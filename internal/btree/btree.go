// Package btree implements a persistent B+tree index built on top of the
// pager package. Each node occupies exactly one page and is (de)serialised by
// the codec. The tree supports insertion with node splitting, lookup, ordered
// range scans, and deletion with sibling borrowing and node merging.
//
// The root page id and the configured order are persisted in a dedicated meta
// page so that a tree can be reopened from an existing pager file.
package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"

	"btree-index/internal/pager"
)

// ErrEmptyKey is returned when an empty key is supplied to Put/Get/Delete.
var ErrEmptyKey = errors.New("btree: empty key")

// ErrInvalidRange is returned when a scan range is inverted (start > end).
var ErrInvalidRange = errors.New("btree: invalid scan range")

// ErrOrder is returned when an unsupported order is configured.
var ErrOrder = errors.New("btree: unsupported order")

// metaPage is the page id of the btree meta page (page 1 in the pager file).
const metaPage = pager.PageID(1)

// DefaultOrder is the default B+tree fan-out (maximum keys per node).
const DefaultOrder = 32

// BTree is a persistent B+tree. All nodes live in the underlying pager.
type BTree struct {
	pg       *pager.Pager
	order    int
	root     pager.PageID
	metaPage pager.PageID
}

// minKeys returns the minimum number of keys a non-root node must retain to
// avoid underflow.
func (t *BTree) minKeys() int { return t.order / 2 }

// Order returns the configured maximum number of keys per node.
func (t *BTree) Order() int { return t.order }

// Root returns the current root page id (0 means an empty tree).
func (t *BTree) Root() pager.PageID { return t.root }

// New creates a brand new btree on the given pager with the requested order and
// writes the initial meta page. The pager must be empty (no existing tree).
func New(pg *pager.Pager, order int) (*BTree, error) {
	if order < 2 {
		return nil, ErrOrder
	}
	t := &BTree{pg: pg, order: order, metaPage: metaPage}
	mid, err := pg.AllocatePage()
	if err != nil {
		return nil, err
	}
	t.metaPage = mid
	t.root = 0
	if err := t.writeMeta(); err != nil {
		return nil, err
	}
	return t, nil
}

// Open reopens an existing btree, reading its order and root page id from the
// meta page. It returns ErrCorrupt if the meta page is not a valid btree page.
func Open(pg *pager.Pager) (*BTree, error) {
	buf, err := pg.ReadPage(metaPage)
	if err != nil {
		return nil, err
	}
	if len(buf) < 16 || binary.LittleEndian.Uint32(buf[0:4]) != btreeMagic {
		return nil, ErrCorrupt
	}
	order := int(binary.LittleEndian.Uint32(buf[4:8]))
	if order < 2 {
		return nil, ErrCorrupt
	}
	root := pager.PageID(binary.LittleEndian.Uint32(buf[12:16]))
	return &BTree{pg: pg, order: order, root: root, metaPage: metaPage}, nil
}

// writeMeta persists the order and root page id into the meta page.
func (t *BTree) writeMeta() error {
	buf := make([]byte, t.pg.PageSize())
	binary.LittleEndian.PutUint32(buf[0:4], btreeMagic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(t.order))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(t.root))
	return t.pg.WritePage(t.metaPage, buf)
}

// load reads and decodes a node from its page.
func (t *BTree) load(pageID pager.PageID) (*node, error) {
	buf, err := t.pg.ReadPage(pageID)
	if err != nil {
		return nil, err
	}
	return decodeNode(buf, pageID)
}

// save encodes and writes a node back to its page.
func (t *BTree) save(n *node) error {
	buf, err := encodeNode(n, t.pg.PageSize())
	if err != nil {
		return err
	}
	return t.pg.WritePage(n.self, buf)
}

// allocSave allocates a fresh page, writes the node there, and returns its id.
func (t *BTree) allocSave(n *node) (pager.PageID, error) {
	id, err := t.pg.AllocatePage()
	if err != nil {
		return 0, err
	}
	n.self = id
	if err := t.save(n); err != nil {
		return 0, err
	}
	return id, nil
}

// Insert inserts or updates key with val.
func (t *BTree) Insert(key, val []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if t.root == 0 {
		n := &node{leaf: true}
		insertLeaf(n, key, val)
		pid, err := t.allocSave(n)
		if err != nil {
			return err
		}
		t.root = pid
		return t.writeMeta()
	}
	pk, rpid, split, err := t.insertRec(t.root, key, val)
	if err != nil {
		return err
	}
	if split {
		nr := &node{
			leaf: false,
			keys: [][]byte{clone(pk)},
			kids: []pager.PageID{t.root, rpid},
		}
		np, err := t.allocSave(nr)
		if err != nil {
			return err
		}
		t.root = np
		return t.writeMeta()
	}
	return nil
}

// insertRec recursively inserts into the subtree rooted at pageID. If the node
// splits, it returns the promoted separator key and the new right sibling page.
func (t *BTree) insertRec(pageID pager.PageID, key, val []byte) (promoteKey []byte, rightPID pager.PageID, split bool, err error) {
	n, err := t.load(pageID)
	if err != nil {
		return nil, 0, false, err
	}
	if n.leaf {
		insertLeaf(n, key, val)
		if len(n.keys) > t.order {
			return t.splitLeaf(n)
		}
		if werr := t.save(n); werr != nil {
			return nil, 0, false, werr
		}
		return nil, 0, false, nil
	}
	ci := childIndex(n, key)
	pk, rpid, split, err := t.insertRec(n.kids[ci], key, val)
	if err != nil {
		return nil, 0, false, err
	}
	if !split {
		return nil, 0, false, nil
	}
	n.keys = insertKeyAt(n.keys, ci, clone(pk))
	n.kids = insertKidAt(n.kids, ci+1, rpid)
	if len(n.keys) > t.order {
		return t.splitInner(n)
	}
	if werr := t.save(n); werr != nil {
		return nil, 0, false, werr
	}
	return nil, 0, false, nil
}

// splitLeaf splits an overfull leaf into left (mutated in place) and right.
func (t *BTree) splitLeaf(n *node) (promoteKey []byte, rightPID pager.PageID, split bool, err error) {
	mid := len(n.keys) / 2
	right := &node{leaf: true}
	right.keys = cloneSlice(n.keys[mid:])
	right.vals = cloneSlice(n.vals[mid:])
	n.keys = n.keys[:mid]
	n.vals = n.vals[:mid]
	rpid, err := t.allocSave(right)
	if err != nil {
		return nil, 0, false, err
	}
	if werr := t.save(n); werr != nil {
		return nil, 0, false, werr
	}
	return clone(right.keys[0]), rpid, true, nil
}

// splitInner splits an overfull inner node, promoting the median key.
func (t *BTree) splitInner(n *node) (promoteKey []byte, rightPID pager.PageID, split bool, err error) {
	mid := len(n.keys) / 2
	promoteKey = clone(n.keys[mid])
	right := &node{leaf: false}
	right.keys = cloneSlice(n.keys[mid+1:])
	right.kids = cloneKids(n.kids[mid+1:])
	n.keys = n.keys[:mid]
	n.kids = n.kids[:mid+1]
	rpid, err := t.allocSave(right)
	if err != nil {
		return nil, 0, false, err
	}
	if werr := t.save(n); werr != nil {
		return nil, 0, false, werr
	}
	return promoteKey, rpid, true, nil
}

// Lookup returns the value stored for key, or (nil, false) if absent.
func (t *BTree) Lookup(key []byte) ([]byte, bool, error) {
	if len(key) == 0 {
		return nil, false, ErrEmptyKey
	}
	if t.root == 0 {
		return nil, false, nil
	}
	n, err := t.load(t.root)
	if err != nil {
		return nil, false, err
	}
	for !n.leaf {
		ci := childIndex(n, key)
		n, err = t.load(n.kids[ci])
		if err != nil {
			return nil, false, err
		}
	}
	idx, found := leafSearch(n, key)
	if found {
		return clone(n.vals[idx]), true, nil
	}
	return nil, false, nil
}

// Delete removes key from the tree. Deleting a missing key is a no-op.
func (t *BTree) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if t.root == 0 {
		return nil
	}
	under, err := t.deleteRec(t.root, key)
	if err != nil {
		return err
	}
	if !under {
		return nil
	}
	root, err := t.load(t.root)
	if err != nil {
		return err
	}
	if root.leaf {
		if len(root.keys) == 0 {
			if ferr := t.pg.FreePage(t.root); ferr != nil {
				return ferr
			}
			t.root = 0
			return t.writeMeta()
		}
		return nil
	}
	if len(root.keys) == 0 {
		newRoot := root.kids[0]
		if ferr := t.pg.FreePage(t.root); ferr != nil {
			return ferr
		}
		t.root = newRoot
		return t.writeMeta()
	}
	return nil
}

// deleteRec removes key from the subtree at pageID. It returns true if the
// node underflowed and its parent must rebalance.
func (t *BTree) deleteRec(pageID pager.PageID, key []byte) (bool, error) {
	n, err := t.load(pageID)
	if err != nil {
		return false, err
	}
	if n.leaf {
		idx, found := leafSearch(n, key)
		if !found {
			return false, nil
		}
		removeKeyAt(n, idx)
		if werr := t.save(n); werr != nil {
			return false, werr
		}
		return len(n.keys) < t.minKeys(), nil
	}
	ci := childIndex(n, key)
	under, err := t.deleteRec(n.kids[ci], key)
	if err != nil {
		return false, err
	}
	if !under {
		return false, nil
	}
	return t.fixUnderflow(n, ci)
}

// fixUnderflow rebalances the subtree at n.kids[idx], which underflowed, by
// borrowing from a sibling or merging with one. It returns true if n itself
// underflowed as a result.
func (t *BTree) fixUnderflow(n *node, idx int) (bool, error) {
	child, err := t.load(n.kids[idx])
	if err != nil {
		return false, err
	}

	// Borrow from the left sibling.
	if idx > 0 {
		left, err := t.load(n.kids[idx-1])
		if err != nil {
			return false, err
		}
		if len(left.keys) > t.minKeys() {
			if left.leaf {
				child.keys = append([][]byte{clone(left.keys[len(left.keys)-1])}, child.keys...)
				child.vals = append([][]byte{clone(left.vals[len(left.vals)-1])}, child.vals...)
				left.keys = left.keys[:len(left.keys)-1]
				left.vals = left.vals[:len(left.vals)-1]
				n.keys[idx-1] = clone(child.keys[0])
			} else {
				child.keys = append([][]byte{clone(n.keys[idx-1])}, child.keys...)
				child.kids = append([]pager.PageID{left.kids[len(left.kids)-1]}, child.kids...)
				n.keys[idx-1] = clone(left.keys[len(left.keys)-1])
				left.keys = left.keys[:len(left.keys)-1]
				left.kids = left.kids[:len(left.kids)-1]
			}
			if werr := t.save(left); werr != nil {
				return false, werr
			}
			if werr := t.save(child); werr != nil {
				return false, werr
			}
			if werr := t.save(n); werr != nil {
				return false, werr
			}
			return false, nil
		}
	}

	// Borrow from the right sibling.
	if idx < len(n.kids)-1 {
		right, err := t.load(n.kids[idx+1])
		if err != nil {
			return false, err
		}
		if len(right.keys) > t.minKeys() {
			if right.leaf {
				child.keys = append(child.keys, clone(right.keys[0]))
				child.vals = append(child.vals, clone(right.vals[0]))
				right.keys = right.keys[1:]
				right.vals = right.vals[1:]
				n.keys[idx] = clone(right.keys[0])
			} else {
				child.keys = append(child.keys, clone(n.keys[idx]))
				child.kids = append(child.kids, right.kids[0])
				n.keys[idx] = clone(right.keys[0])
				right.keys = right.keys[1:]
				right.kids = right.kids[1:]
			}
			if werr := t.save(right); werr != nil {
				return false, werr
			}
			if werr := t.save(child); werr != nil {
				return false, werr
			}
			if werr := t.save(n); werr != nil {
				return false, werr
			}
			return false, nil
		}
	}

	// Merge with a sibling.
	if idx > 0 {
		left, err := t.load(n.kids[idx-1])
		if err != nil {
			return false, err
		}
		if merr := t.mergeNodes(n, idx-1, left, child); merr != nil {
			return false, merr
		}
	} else {
		right, err := t.load(n.kids[idx+1])
		if err != nil {
			return false, err
		}
		if merr := t.mergeNodes(n, idx, child, right); merr != nil {
			return false, merr
		}
	}
	return len(n.keys) < t.minKeys(), nil
}

// mergeNodes merges right into left, pulling down the separator key at
// n.keys[sepIdx], then removes that separator and the right child pointer from
// the parent n and frees the right page.
func (t *BTree) mergeNodes(n *node, sepIdx int, left, right *node) error {
	if left.leaf {
		left.keys = append(left.keys, right.keys...)
		left.vals = append(left.vals, right.vals...)
	} else {
		left.keys = append(left.keys, clone(n.keys[sepIdx]))
		left.keys = append(left.keys, right.keys...)
		left.kids = append(left.kids, right.kids...)
	}
	n.keys = append(n.keys[:sepIdx], n.keys[sepIdx+1:]...)
	n.kids = append(n.kids[:sepIdx+1], n.kids[sepIdx+2:]...)
	if werr := t.save(left); werr != nil {
		return werr
	}
	if werr := t.save(n); werr != nil {
		return werr
	}
	return t.pg.FreePage(right.self)
}

// Scan returns all key/value pairs whose key lies in [start, end], in ascending
// key order. start must not be greater than end.
func (t *BTree) Scan(start, end []byte) ([][]byte, [][]byte, error) {
	if bytes.Compare(start, end) > 0 {
		return nil, nil, ErrInvalidRange
	}
	if t.root == 0 {
		return nil, nil, nil
	}
	var ks, vs [][]byte
	if err := t.scanRec(t.root, start, end, &ks, &vs); err != nil {
		return nil, nil, err
	}
	return ks, vs, nil
}

// scanRec collects every key/value pair in range from the subtree at pageID.
func (t *BTree) scanRec(pageID pager.PageID, start, end []byte, ks, vs *[][]byte) error {
	n, err := t.load(pageID)
	if err != nil {
		return err
	}
	if n.leaf {
		for i := range n.keys {
			if bytes.Compare(n.keys[i], start) >= 0 && bytes.Compare(n.keys[i], end) <= 0 {
				*ks = append(*ks, clone(n.keys[i]))
				*vs = append(*vs, clone(n.vals[i]))
			}
		}
		return nil
	}
	for i := range n.kids {
		if err := t.scanRec(n.kids[i], start, end, ks, vs); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the total number of key/value pairs in the tree by performing a
// full ordered scan bounded by the smallest and largest possible keys.
func (t *BTree) Count() (int, error) {
	ks, _, err := t.Scan([]byte{0}, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if err != nil {
		return 0, err
	}
	return len(ks), nil
}

// ensure sort is referenced even if unused by future refactors.
var _ = sort.Search

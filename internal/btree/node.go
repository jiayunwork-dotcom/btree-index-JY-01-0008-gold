package btree

import (
	"bytes"
	"sort"

	"btree-index/internal/pager"
)

// node is an in-memory representation of a B+tree node. A node is either a
// leaf (storing key/value pairs) or an inner node (storing separator keys and
// child page ids). Every node is serialised into exactly one page.
type node struct {
	leaf bool
	keys [][]byte        // sorted ascending; empty for an empty node
	vals [][]byte        // leaf values, parallel to keys
	kids []pager.PageID  // inner child page ids, len == len(keys)+1
	self pager.PageID    // page id where this node is stored (0 until persisted)
}

// clone returns a copy of a byte slice, or nil for a nil input.
func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// cloneSlice deep-copies a slice of byte slices.
func cloneSlice(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = clone(in[i])
	}
	return out
}

// cloneKids copies a slice of page ids.
func cloneKids(in []pager.PageID) []pager.PageID {
	out := make([]pager.PageID, len(in))
	copy(out, in)
	return out
}

// childIndex returns the index of the child that should contain key, using the
// invariant that keys[i] is the smallest key of subtree kids[i+1].
func childIndex(n *node, key []byte) int {
	return sort.Search(len(n.keys), func(i int) bool {
		return bytes.Compare(key, n.keys[i]) < 0
	})
}

// leafSearch returns the index of key within a leaf, or (-1, false) if absent.
func leafSearch(n *node, key []byte) (int, bool) {
	idx := sort.Search(len(n.keys), func(i int) bool {
		return bytes.Compare(n.keys[i], key) >= 0
	})
	if idx < len(n.keys) && bytes.Equal(n.keys[idx], key) {
		return idx, true
	}
	return -1, false
}

// insertLeaf inserts or updates a key/value pair inside a leaf node. The node's
// keys slice is kept sorted in ascending order.
func insertLeaf(n *node, key, val []byte) {
	idx := sort.Search(len(n.keys), func(i int) bool {
		return bytes.Compare(n.keys[i], key) >= 0
	})
	if idx < len(n.keys) && bytes.Equal(n.keys[idx], key) {
		n.vals[idx] = clone(val)
		return
	}
	n.keys = append(n.keys, nil)
	copy(n.keys[idx+1:], n.keys[idx:])
	n.keys[idx] = clone(key)
	n.vals = append(n.vals, nil)
	copy(n.vals[idx+1:], n.vals[idx:])
	n.vals[idx] = clone(val)
}

// insertKeyAt inserts k at position i of a key slice.
func insertKeyAt(keys [][]byte, i int, k []byte) [][]byte {
	keys = append(keys, nil)
	copy(keys[i+1:], keys[i:])
	keys[i] = k
	return keys
}

// insertKidAt inserts a child page id at position i of a kids slice.
func insertKidAt(kids []pager.PageID, i int, k pager.PageID) []pager.PageID {
	kids = append(kids, 0)
	copy(kids[i+1:], kids[i:])
	kids[i] = k
	return kids
}

// removeKeyAt removes the key (and parallel value) at position i of a leaf.
func removeKeyAt(n *node, i int) {
	n.keys = append(n.keys[:i], n.keys[i+1:]...)
	n.vals = append(n.vals[:i], n.vals[i+1:]...)
}

// Package index provides a high-level persistent ordered key/value facade built
// on the pager and btree packages. It opens (or creates) a page file, manages
// the B+tree that lives inside it, and exposes Put/Get/Delete/Scan operations
// with ordered iteration. Reopening an index from the same path yields the data
// that was persisted by a previous session.
package index

import (
	"errors"

	"btree-index/internal/btree"
	"btree-index/internal/pager"
)

// ErrEmptyKey is returned when an empty key is supplied to Put/Get/Delete.
var ErrEmptyKey = errors.New("index: empty key")

// KV is a key/value pair returned by Scan.
type KV struct {
	Key   []byte
	Value []byte
}

// Index is a persistent ordered key/value store.
type Index struct {
	pg    *pager.Pager
	tree  *btree.BTree
	path  string
	order int
}

// Open opens (creating if necessary) an index at path using the default order.
func Open(path string) (*Index, error) {
	return OpenWithOrder(path, btree.DefaultOrder)
}

// OpenWithOrder opens an index at path, creating it with the given order when
// the file does not yet contain a tree. Existing files keep their stored order.
func OpenWithOrder(path string, order int) (*Index, error) {
	var pg *pager.Pager
	var err error
	pg, err = pager.Open(path)
	if err != nil {
		pg, err = pager.Create(path, pager.DefaultPageSize)
		if err != nil {
			return nil, err
		}
	}
	var tree *btree.BTree
	tree, err = btree.Open(pg)
	if err != nil {
		// A fresh pager file has no tree yet: initialise one.
		tree, err = btree.New(pg, order)
		if err != nil {
			_ = pg.Close()
			return nil, err
		}
	}
	return &Index{pg: pg, tree: tree, path: path, order: order}, nil
}

// Path returns the underlying file path.
func (i *Index) Path() string { return i.path }

// Order returns the configured B+tree order.
func (i *Index) Order() int { return i.order }

// Put inserts or updates the value for key.
func (i *Index) Put(key, value []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	return i.tree.Insert(key, value)
}

// Get returns the value stored for key, or (nil, false) if absent.
func (i *Index) Get(key []byte) ([]byte, bool, error) {
	if len(key) == 0 {
		return nil, false, ErrEmptyKey
	}
	return i.tree.Lookup(key)
}

// Delete removes key from the index. Deleting a missing key is a no-op.
func (i *Index) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	return i.tree.Delete(key)
}

// Scan returns all key/value pairs whose key lies in [start, end], in ascending
// key order. start must not be greater than end.
func (i *Index) Scan(start, end []byte) ([]KV, error) {
	ks, vs, err := i.tree.Scan(start, end)
	if err != nil {
		return nil, err
	}
	out := make([]KV, len(ks))
	for j := range ks {
		out[j] = KV{Key: ks[j], Value: vs[j]}
	}
	return out, nil
}

// Count returns the number of key/value pairs in the index.
func (i *Index) Count() (int, error) {
	return i.tree.Count()
}

// Close flushes any pending state to disk and closes the underlying file.
func (i *Index) Close() error {
	if i.pg == nil {
		return nil
	}
	err := i.pg.Close()
	i.pg = nil
	return err
}

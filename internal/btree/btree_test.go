package btree

import (
	"bytes"
	"path/filepath"
	"sort"
	"testing"

	"btree-index/internal/pager"
)

// newTestTree builds a btree backed by a fresh temporary pager with the given
// order and returns the tree and a cleanup function.
func newTestTree(t *testing.T, order int) (*BTree, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "btree.db")
	pg, err := pager.Create(path, 4096)
	if err != nil {
		t.Fatalf("create pager: %v", err)
	}
	tr, err := New(pg, order)
	if err != nil {
		_ = pg.Close()
		t.Fatalf("new btree: %v", err)
	}
	return tr, func() { _ = pg.Close() }
}

// kv builds a deterministic key/value pair for index i.
func kv(i int) ([]byte, []byte) {
	k := []byte{byte(i >> 8), byte(i)}
	v := []byte{byte(i), byte(i >> 8), 0xAB}
	return k, v
}

// kkey returns just the key component of kv(i).
func kkey(i int) []byte {
	k, _ := kv(i)
	return k
}

// TestBTreeInsertLookup verifies insertion, exact lookup, value update and the
// absence of missing keys.
func TestBTreeInsertLookup(t *testing.T) {
	tr, cleanup := newTestTree(t, DefaultOrder)
	defer cleanup()

	const n = 500
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if err := tr.Insert(k, v); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Look up every inserted key.
	for i := 0; i < n; i++ {
		k, want := kv(i)
		got, ok, err := tr.Lookup(k)
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("lookup %d: key not found", i)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("lookup %d: value mismatch", i)
		}
	}
	// Missing keys return not found.
	miss := []byte{0xFF, 0xFF}
	if _, ok, err := tr.Lookup(miss); err != nil || ok {
		t.Fatalf("lookup missing: expected (nil,false), got (_,%v,err=%v)", ok, err)
	}
	// Update an existing key and confirm the new value.
	uk, _ := kv(42)
	uv2 := []byte("updated-value")
	if err := tr.Insert(uk, uv2); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok, err := tr.Lookup(uk)
	if err != nil || !ok || !bytes.Equal(got, uv2) {
		t.Fatalf("update lookup mismatch: got=%v ok=%v err=%v", got, ok, err)
	}
	// Empty key is rejected.
	if err := tr.Insert(nil, []byte("x")); err != ErrEmptyKey {
		t.Fatalf("empty key expected ErrEmptyKey, got %v", err)
	}
	if _, _, err := tr.Lookup(nil); err != ErrEmptyKey {
		t.Fatalf("empty key lookup expected ErrEmptyKey, got %v", err)
	}
}

// TestBTreeSplit forces node splits with a small order and verifies the tree
// stays fully searchable and ordered.
func TestBTreeSplit(t *testing.T) {
	const order = 4
	tr, cleanup := newTestTree(t, order)
	defer cleanup()

	const n = 200
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if err := tr.Insert(k, v); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// The root must have been split into an inner node.
	if tr.root == 0 {
		t.Fatalf("root should be non-empty after splits")
	}
	root, err := tr.load(tr.root)
	if err != nil {
		t.Fatalf("load root: %v", err)
	}
	if root.leaf {
		t.Fatalf("root should be an inner node after %d inserts with order %d", n, order)
	}
	// Every key must remain findable.
	for i := 0; i < n; i++ {
		k, want := kv(i)
		got, ok, lerr := tr.Lookup(k)
		if lerr != nil || !ok {
			t.Fatalf("lookup %d: not found (leaf=%v err=%v)", i, ok, lerr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("lookup %d: value mismatch", i)
		}
	}
	// Ordered scan must return all keys in ascending order.
	ks, vs, err := tr.Scan([]byte{0}, []byte{0xff, 0xff})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(ks) != n {
		t.Fatalf("scan count = %d, want %d", len(ks), n)
	}
	for i := 1; i < len(ks); i++ {
		if bytes.Compare(ks[i-1], ks[i]) >= 0 {
			t.Fatalf("scan not sorted at %d", i)
		}
	}
	_, want100 := kv(100)
	if !bytes.Equal(vs[100], want100) {
		t.Fatalf("scan value mismatch at 100")
	}
}

// TestBTreeDeleteMerge exercises deletion with sibling borrowing and node
// merging using a small order so structural rebalancing is forced.
func TestBTreeDeleteMerge(t *testing.T) {
	const order = 4
	tr, cleanup := newTestTree(t, order)
	defer cleanup()

	const n = 120
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if err := tr.Insert(k, v); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Delete every key in a shuffled-ish order (reverse) to force merges.
	for i := n - 1; i >= 0; i-- {
		k, _ := kv(i)
		if err := tr.Delete(k); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
		got, ok, lerr := tr.Lookup(k)
		if lerr != nil {
			t.Fatalf("lookup after delete %d: %v", i, lerr)
		}
		if ok || got != nil {
			t.Fatalf("delete %d: key still present", i)
		}
		// Remaining keys must still be present and correct.
		if i%7 == 0 {
			for j := 0; j < i; j++ {
				jk, jv := kv(j)
				g, ok2, e2 := tr.Lookup(jk)
				if e2 != nil {
					t.Fatalf("post-delete lookup %d: %v", j, e2)
				}
				if j < i && (!ok2 || !bytes.Equal(g, jv)) {
					t.Fatalf("post-delete lookup %d: missing/incorrect", j)
				}
			}
		}
	}

	// The tree should be empty now.
	if tr.root != 0 {
		root, rerr := tr.load(tr.root)
		if rerr != nil {
			t.Fatalf("load root: %v", rerr)
		}
		if len(root.keys) != 0 {
			t.Fatalf("expected empty tree, root has %d keys", len(root.keys))
		}
	}
	cnt, err := tr.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected count 0, got %d", cnt)
	}
}

// TestBTreeDeletePartial removes only a middle subset, checking that borrows
// and merges keep the remaining keys reachable.
func TestBTreeDeletePartial(t *testing.T) {
	const order = 5
	tr, cleanup := newTestTree(t, order)
	defer cleanup()

	const n = 300
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if err := tr.Insert(k, v); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Remove keys in the middle range.
	for i := 100; i < 200; i++ {
		k, _ := kv(i)
		if err := tr.Delete(k); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	// Keys outside the range are intact.
	for i := 0; i < n; i++ {
		k, want := kv(i)
		g, ok, lerr := tr.Lookup(k)
		if lerr != nil {
			t.Fatalf("lookup %d: %v", i, lerr)
		}
		if i >= 100 && i < 200 {
			if ok {
				t.Fatalf("key %d should have been deleted", i)
			}
			continue
		}
		if !ok || !bytes.Equal(g, want) {
			t.Fatalf("key %d missing or wrong after partial delete", i)
		}
	}
	// Scan of the surviving range matches expectations.
	ks, _, err := tr.Scan(kkey(0), kkey(n-1))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(ks) != n-100 {
		t.Fatalf("scan count = %d, want %d", len(ks), n-100)
	}
	// Confirm ordering of the surviving keys.
	sorted := make([][]byte, len(ks))
	copy(sorted, ks)
	sort.Slice(sorted, func(a, b int) bool { return bytes.Compare(sorted[a], sorted[b]) < 0 })
	for i := range ks {
		if !bytes.Equal(ks[i], sorted[i]) {
			t.Fatalf("scan not in ascending order at %d", i)
		}
	}
}

// TestBTreeScanRange verifies inclusive range boundaries and rejection of an
// inverted range.
func TestBTreeScanRange(t *testing.T) {
	tr, cleanup := newTestTree(t, DefaultOrder)
	defer cleanup()

	const n = 50
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if err := tr.Insert(k, v); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// Inclusive range [10, 20] should yield 11 keys.
	ks, _, err := tr.Scan(kkey(10), kkey(20))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(ks) != 11 {
		t.Fatalf("inclusive range count = %d, want 11", len(ks))
	}
	// Inverted range is rejected.
	if _, _, err := tr.Scan(kkey(20), kkey(10)); err != ErrInvalidRange {
		t.Fatalf("inverted range expected ErrInvalidRange, got %v", err)
	}
	// Empty range single key.
	single, _, err := tr.Scan(kkey(5), kkey(5))
	if err != nil {
		t.Fatalf("scan single: %v", err)
	}
	if len(single) != 1 {
		t.Fatalf("single key scan count = %d, want 1", len(single))
	}
}

// TestBTreeReopenWithDeletes checks that a tree which has been split and merged
// still persists correctly across a pager reopen.
func TestBTreeReopenWithDeletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "btree.db")

	pg, err := pager.Create(path, 4096)
	if err != nil {
		t.Fatalf("create pager: %v", err)
	}
	tr, err := New(pg, 6)
	if err != nil {
		_ = pg.Close()
		t.Fatalf("new btree: %v", err)
	}
	const n = 80
	for i := 0; i < n; i++ {
		k, v := kv(i)
		if ierr := tr.Insert(k, v); ierr != nil {
			t.Fatalf("insert: %v", ierr)
		}
	}
	for i := 0; i < n; i += 2 {
		k, _ := kv(i)
		if derr := tr.Delete(k); derr != nil {
			t.Fatalf("delete: %v", derr)
		}
	}
	if cerr := pg.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Reopen.
	pg2, err := pager.Open(path)
	if err != nil {
		t.Fatalf("reopen pager: %v", err)
	}
	defer func() { _ = pg2.Close() }()
	tr2, err := Open(pg2)
	if err != nil {
		t.Fatalf("reopen btree: %v", err)
	}
	for i := 0; i < n; i++ {
		k, want := kv(i)
		g, ok, lerr := tr2.Lookup(k)
		if lerr != nil {
			t.Fatalf("reopen lookup %d: %v", i, lerr)
		}
		if i%2 == 0 {
			if ok {
				t.Fatalf("reopen: even key %d should be deleted", i)
			}
		} else {
			if !ok || !bytes.Equal(g, want) {
				t.Fatalf("reopen: odd key %d missing/wrong", i)
			}
		}
	}
}

package index

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestIndexPutGet verifies basic insert, lookup, update and deletion through the
// index facade.
func TestIndexPutGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.db")
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	// Put a handful of keys.
	pairs := map[string]string{
		"alpha":   "1",
		"beta":    "2",
		"gamma":   "3",
		"delta":   "4",
	}
	for k, v := range pairs {
		if perr := idx.Put([]byte(k), []byte(v)); perr != nil {
			t.Fatalf("put %q: %v", k, perr)
		}
	}
	// Every key must be retrievable.
	for k, v := range pairs {
		got, ok, gerr := idx.Get([]byte(k))
		if gerr != nil {
			t.Fatalf("get %q: %v", k, gerr)
		}
		if !ok {
			t.Fatalf("get %q: not found", k)
		}
		if string(got) != v {
			t.Fatalf("get %q: got %q want %q", k, got, v)
		}
	}
	// Missing key returns not found.
	if _, ok, _ := idx.Get([]byte("zzz")); ok {
		t.Fatalf("get missing key should be false")
	}
	// Update an existing key.
	if err := idx.Put([]byte("beta"), []byte("20")); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok, _ := idx.Get([]byte("beta"))
	if !ok || string(got) != "20" {
		t.Fatalf("update failed: got %q", got)
	}
	// Delete a key.
	if err := idx.Delete([]byte("alpha")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := idx.Get([]byte("alpha")); ok {
		t.Fatalf("alpha should be deleted")
	}
	// The remaining keys are still present.
	for _, k := range []string{"beta", "gamma", "delta"} {
		if _, ok, _ := idx.Get([]byte(k)); !ok {
			t.Fatalf("key %q lost after delete", k)
		}
	}
	// Empty key is rejected.
	if err := idx.Put(nil, []byte("x")); err != ErrEmptyKey {
		t.Fatalf("put empty key expected ErrEmptyKey, got %v", err)
	}
}

// TestIndexRangeScan verifies ordered range scans through the facade.
func TestIndexRangeScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.db")
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	const n = 100
	for i := 0; i < n; i++ {
		k := []byte{byte(i >> 8), byte(i)}
		v := []byte{byte(i)}
		if perr := idx.Put(k, v); perr != nil {
			t.Fatalf("put %d: %v", i, perr)
		}
	}
	cnt, err := idx.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != n {
		t.Fatalf("count = %d, want %d", cnt, n)
	}
	// Inclusive scan [10, 20].
	res, err := idx.Scan([]byte{0, 10}, []byte{0, 20})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res) != 11 {
		t.Fatalf("scan width = %d, want 11", len(res))
	}
	for j, kv := range res {
		want := byte(10 + j)
		if kv.Key[0] != 0 || kv.Key[1] != want {
			t.Fatalf("scan key[%d] = %v, want {0,%d}", j, kv.Key, want)
		}
		if kv.Value[0] != want {
			t.Fatalf("scan value[%d] = %v, want %d", j, kv.Value, want)
		}
	}
	// Results must be in ascending key order.
	for j := 1; j < len(res); j++ {
		if bytes.Compare(res[j-1].Key, res[j].Key) >= 0 {
			t.Fatalf("scan not ordered at %d", j)
		}
	}
}

// TestIndexPersistReopen verifies that data written by one session is readable
// after closing and reopening the same file from disk.
func TestIndexPersistReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const n = 250
	want := map[string]string{}
	for i := 0; i < n; i++ {
		k := []byte{byte(i >> 8), byte(i)}
		v := []byte{byte(i), byte(i >> 4)}
		want[string(k)] = string(v)
		if perr := idx.Put(k, v); perr != nil {
			t.Fatalf("put %d: %v", i, perr)
		}
	}
	// Delete a few so the reopen also exercises a non-trivial tree.
	for i := 0; i < n; i += 10 {
		k := []byte{byte(i >> 8), byte(i)}
		if derr := idx.Delete(k); derr != nil {
			t.Fatalf("delete %d: %v", i, derr)
		}
		delete(want, string(k))
	}
	if cerr := idx.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Reopen the same file.
	idx2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = idx2.Close() }()

	// Every surviving key must survive the round trip.
	for k, v := range want {
		g, ok, gerr := idx2.Get([]byte(k))
		if gerr != nil {
			t.Fatalf("reopen get %q: %v", k, gerr)
		}
		if !ok {
			t.Fatalf("reopen: key %q missing", k)
		}
		if string(g) != v {
			t.Fatalf("reopen: key %q value %q want %q", k, g, v)
		}
	}
	// Count must match the surviving set.
	cnt, err := idx2.Count()
	if err != nil {
		t.Fatalf("reopen count: %v", err)
	}
	if cnt != len(want) {
		t.Fatalf("reopen count = %d, want %d", cnt, len(want))
	}
	// A range scan must still be ordered and complete after reopening.
	all, err := idx2.Scan([]byte{0, 0}, []byte{0xff, 0xff})
	if err != nil {
		t.Fatalf("reopen scan: %v", err)
	}
	if len(all) != len(want) {
		t.Fatalf("reopen scan width = %d, want %d", len(all), len(want))
	}
	for j := 1; j < len(all); j++ {
		if bytes.Compare(all[j-1].Key, all[j].Key) >= 0 {
			t.Fatalf("reopen scan not ordered at %d", j)
		}
	}
}

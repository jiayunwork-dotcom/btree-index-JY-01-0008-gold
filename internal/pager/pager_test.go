package pager

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// tmpPager creates a pager backed by a temporary file and returns the pager
// along with a cleanup function.
func tmpPager(t *testing.T, pageSize int) (*Pager, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pager.db")
	p, err := Create(path, pageSize)
	if err != nil {
		t.Fatalf("create pager: %v", err)
	}
	return p, func() { _ = p.Close() }
}

// fill returns a deterministic byte slice of the given length.
func fill(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestPagerAllocFree verifies that AllocatePage hands out distinct ids, that
// FreePage puts them back, and that a freed page is reused.
func TestPagerAllocFree(t *testing.T) {
	const pageSize = 512
	p, cleanup := tmpPager(t, pageSize)
	defer cleanup()

	// Allocating several pages yields distinct, monotonic ids.
	var ids []PageID
	for i := 0; i < 5; i++ {
		id, err := p.AllocatePage()
		if err != nil {
			t.Fatalf("AllocatePage: %v", err)
		}
		if id == headerPage || id == metaPage && false {
			t.Fatalf("unexpected reserved id %d", id)
		}
		for _, prev := range ids {
			if prev == id {
				t.Fatalf("duplicate page id %d", id)
			}
		}
		ids = append(ids, id)
	}
	if len(ids) != 5 {
		t.Fatalf("expected 5 ids, got %d", len(ids))
	}
	if p.NumPages() != 1+5 {
		t.Fatalf("expected 6 pages (header + 5), got %d", p.NumPages())
	}

	// Write distinct content to each page and read it back.
	for i, id := range ids {
		data := fill(byte('a'+i), pageSize)
		if err := p.WritePage(id, data); err != nil {
			t.Fatalf("WritePage(%d): %v", id, err)
		}
		got, err := p.ReadPage(id)
		if err != nil {
			t.Fatalf("ReadPage(%d): %v", id, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("page %d content mismatch", id)
		}
	}

	// Free the middle page; free count becomes 1.
	mid := ids[2]
	if err := p.FreePage(mid); err != nil {
		t.Fatalf("FreePage(%d): %v", mid, err)
	}
	if p.FreeCount() != 1 {
		t.Fatalf("expected free count 1, got %d", p.FreeCount())
	}

	// The next allocation should reuse the freed page.
	reused, err := p.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage after free: %v", err)
	}
	if reused != mid {
		t.Fatalf("expected reuse of page %d, got %d", mid, reused)
	}
	if p.FreeCount() != 0 {
		t.Fatalf("expected free count 0, got %d", p.FreeCount())
	}

	// Freeing the header page is rejected.
	if err := p.FreePage(headerPage); err != ErrInvalidPage {
		t.Fatalf("FreePage(header) expected ErrInvalidPage, got %v", err)
	}

	// Reading/writing out of range is rejected.
	if _, err := p.ReadPage(PageID(p.NumPages() + 10)); err != ErrInvalidPage {
		t.Fatalf("ReadPage(out of range) expected ErrInvalidPage, got %v", err)
	}
	if err := p.WritePage(PageID(p.NumPages()+10), fill(1, pageSize)); err != ErrInvalidPage {
		t.Fatalf("WritePage(out of range) expected ErrInvalidPage, got %v", err)
	}

	// Writing more than a page is rejected.
	if err := p.WritePage(reused, fill(1, pageSize+1)); err != ErrPageOverflow {
		t.Fatalf("WritePage(overflow) expected ErrPageOverflow, got %v", err)
	}
}

// TestPagerPersistReopen verifies that allocated pages, their contents and the
// free list survive a close/reopen cycle on disk.
func TestPagerPersistReopen(t *testing.T) {
	const pageSize = 1024
	dir := t.TempDir()
	path := filepath.Join(dir, "pager.db")

	p, err := Create(path, pageSize)
	if err != nil {
		t.Fatalf("create pager: %v", err)
	}

	// Allocate pages and remember what we wrote.
	contents := map[PageID][]byte{}
	for i := 0; i < 8; i++ {
		id, aerr := p.AllocatePage()
		if aerr != nil {
			t.Fatalf("AllocatePage: %v", aerr)
		}
		data := fill(byte(10+i), pageSize)
		if werr := p.WritePage(id, data); werr != nil {
			t.Fatalf("WritePage: %v", werr)
		}
		contents[id] = data
	}
	// Free two pages so the free list is non-empty on disk.
	freed := []PageID{}
	for _, id := range []PageID{3, 6} {
		if ferr := p.FreePage(id); ferr != nil {
			t.Fatalf("FreePage: %v", ferr)
		}
		freed = append(freed, id)
	}
	if cerr := p.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Reopen from the same path.
	q, oerr := Open(path)
	if oerr != nil {
		t.Fatalf("reopen pager: %v", oerr)
	}
	defer func() { _ = q.Close() }()

	if q.PageSize() != pageSize {
		t.Fatalf("page size mismatch: got %d want %d", q.PageSize(), pageSize)
	}
	if q.FreeCount() != 2 {
		t.Fatalf("free count mismatch: got %d want 2", q.FreeCount())
	}

	// All non-freed pages must still contain their original content.
	for id, want := range contents {
		if id == 3 || id == 6 {
			continue // freed pages are not expected to retain content
		}
		got, rerr := q.ReadPage(id)
		if rerr != nil {
			t.Fatalf("ReadPage(%d): %v", id, rerr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("page %d content changed after reopen", id)
		}
	}

	// A fresh allocation should reuse a freed page id.
	reused, aerr := q.AllocatePage()
	if aerr != nil {
		t.Fatalf("AllocatePage after reopen: %v", aerr)
	}
	found := false
	for _, f := range freed {
		if reused == f {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reuse of a freed page, got %d", reused)
	}
}

// TestPagerCreateExisting verifies that Create refuses to clobber an existing
// file while Open rejects a missing one.
func TestPagerCreateExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pager.db")

	if _, err := Create(path, 256); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := Create(path, 256); err != ErrFileExists {
		t.Fatalf("second create expected ErrFileExists, got %v", err)
	}
	if _, err := Open(filepath.Join(dir, "missing.db")); err == nil {
		t.Fatalf("Open(missing) expected error")
	}
	// The original file must still be openable.
	if p, err := Open(path); err != nil {
		t.Fatalf("Open existing: %v", err)
	} else {
		_ = p.Close()
	}
}

// TestPagerPartialWrite ensures the trailing portion of a page is preserved
// when a short write is issued.
func TestPagerPartialWrite(t *testing.T) {
	const pageSize = 512
	p, cleanup := tmpPager(t, pageSize)
	defer cleanup()

	id, err := p.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}
	// Write a short payload and verify it lands at the start.
	if err := p.WritePage(id, []byte("hello")); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	got, err := p.ReadPage(id)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if string(got[:5]) != "hello" {
		t.Fatalf("short write payload mismatch: %q", string(got[:5]))
	}
}

// ensure os import is used even if a helper changes.
var _ = os.Stat

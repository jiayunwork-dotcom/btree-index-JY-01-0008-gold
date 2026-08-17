// Package pager implements a fixed-size page file manager used as the
// storage layer for persistent data structures.
//
// The on-disk file is divided into fixed-size pages. Page 0 is a reserved
// header page that stores the page size, the total number of allocated pages
// and the head of a free-list chain. Pages 1..N-1 are user pages that can be
// allocated, written, read and freed.
//
// Free pages form a singly-linked chain: the first four bytes of a free page
// store the page id of the next free page (0 means end of chain). The head of
// the chain and the total free count are mirrored in the header so that the
// manager can be reopened from an existing file without scanning the chain.
package pager

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// PageID is the 1-based identifier of a page. Page 0 is the header and is
// never handed out by AllocatePage.
type PageID uint32

// Well-known page ids.
const (
	headerPage PageID = 0
	metaPage   PageID = 1
)

// Magic number stored in the first four bytes of the header page.
const pagerMagic = 0x50414752 // "PAGR"

// pagerVersion is the on-disk format version.
const pagerVersion = 1

// ErrInvalidPage is returned when a page id is out of range or reserved.
var ErrInvalidPage = errors.New("pager: invalid page id")

// ErrPageOverflow is returned when data does not fit in a single page.
var ErrPageOverflow = errors.New("pager: data exceeds page size")

// ErrFileExists is returned by Create when the target file already exists.
var ErrFileExists = errors.New("pager: file already exists")

// ErrCorrupt is returned when an existing file is not a valid pager file.
var ErrCorrupt = errors.New("pager: corrupt header")

// Pager is a fixed-size page file manager.
type Pager struct {
	path      string
	f         *os.File
	pageSize  int
	numPages  int
	freeHead  PageID
	freeCount int
}

// DefaultPageSize is the default page size in bytes.
const DefaultPageSize = 4096

// PageSize returns the size of a single page in bytes.
func (p *Pager) PageSize() int { return p.pageSize }

// NumPages returns the total number of pages currently allocated in the file
// (including the header page).
func (p *Pager) NumPages() int { return p.numPages }

// FreeCount returns the number of pages currently on the free list.
func (p *Pager) FreeCount() int { return p.freeCount }

// Path returns the underlying file path.
func (p *Pager) Path() string { return p.path }

// Create creates a new pager file at path with the given page size. It
// returns ErrFileExists if the file already exists.
func Create(path string, pageSize int) (*Pager, error) {
	if pageSize <= 0 {
		return nil, fmt.Errorf("pager: invalid page size %d", pageSize)
	}
	if _, err := os.Stat(path); err == nil {
		return nil, ErrFileExists
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	p := &Pager{
		path:     path,
		f:        f,
		pageSize: pageSize,
		numPages: 1, // header page 0 exists
	}
	if err := p.Flush(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return p, nil
}

// Open opens an existing pager file. It validates the header and reloads the
// in-memory free-list state from the header page.
func Open(path string) (*Pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 8)
	if _, err := f.ReadAt(hdr, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	if binary.LittleEndian.Uint32(hdr[0:4]) != pagerMagic {
		_ = f.Close()
		return nil, ErrCorrupt
	}
	p := &Pager{path: path, f: f}
	if err := p.readHeader(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return p, nil
}

// readHeader loads the header page into the in-memory state. The header fields
// all fit within the first 24 bytes, so a fixed-size buffer is used; this avoids
// depending on p.pageSize which is itself read from the header.
func (p *Pager) readHeader() error {
	buf := make([]byte, 24)
	if _, err := p.f.ReadAt(buf, 0); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(buf[0:4]) != pagerMagic {
		return ErrCorrupt
	}
	p.pageSize = int(binary.LittleEndian.Uint32(buf[4:8]))
	if p.pageSize <= 0 {
		return ErrCorrupt
	}
	ver := binary.LittleEndian.Uint32(buf[8:12])
	if ver != pagerVersion {
		return ErrCorrupt
	}
	p.numPages = int(binary.LittleEndian.Uint32(buf[12:16]))
	p.freeHead = PageID(binary.LittleEndian.Uint32(buf[16:20]))
	p.freeCount = int(binary.LittleEndian.Uint32(buf[20:24]))
	if p.numPages < 1 {
		return ErrCorrupt
	}
	return nil
}

// writeHeader serialises the in-memory state into the header page.
func (p *Pager) writeHeader() error {
	buf := make([]byte, p.pageSize)
	binary.LittleEndian.PutUint32(buf[0:4], pagerMagic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(p.pageSize))
	binary.LittleEndian.PutUint32(buf[8:12], pagerVersion)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(p.numPages))
	binary.LittleEndian.PutUint32(buf[16:20], uint32(p.freeHead))
	binary.LittleEndian.PutUint32(buf[20:24], uint32(p.freeCount))
	_, err := p.f.WriteAt(buf, 0)
	return err
}

// grow ensures the file is large enough to contain page pageID.
func (p *Pager) grow(pageID PageID) error {
	off := int64(pageID) * int64(p.pageSize)
	fi, err := p.f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() >= off+int64(p.pageSize) {
		return nil
	}
	zeros := make([]byte, p.pageSize)
	_, err = p.f.WriteAt(zeros, off)
	return err
}

// AllocatePage returns a previously freed page if available, otherwise it
// extends the file and returns a brand new page id.
func (p *Pager) AllocatePage() (PageID, error) {
	if p.freeHead != 0 {
		id := p.freeHead
		buf, err := p.ReadPage(id)
		if err != nil {
			return 0, err
		}
		p.freeHead = PageID(binary.LittleEndian.Uint32(buf[0:4]))
		if p.freeCount > 0 {
			p.freeCount--
		}
		return id, nil
	}
	id := PageID(p.numPages)
	p.numPages++
	if err := p.grow(id); err != nil {
		return 0, err
	}
	return id, nil
}

// ReadPage reads the full contents of page id. The returned slice is owned by
// the caller and may be modified freely.
func (p *Pager) ReadPage(id PageID) ([]byte, error) {
	if int(id) >= p.numPages {
		return nil, ErrInvalidPage
	}
	buf := make([]byte, p.pageSize)
	_, err := p.f.ReadAt(buf, int64(id)*int64(p.pageSize))
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// WritePage writes data to page id. data must not be larger than the page
// size; it may be smaller, in which case the trailing bytes of the page are
// left untouched.
func (p *Pager) WritePage(id PageID, data []byte) error {
	if int(id) >= p.numPages {
		return ErrInvalidPage
	}
	if len(data) > p.pageSize {
		return ErrPageOverflow
	}
	_, err := p.f.WriteAt(data, int64(id)*int64(p.pageSize))
	return err
}

// FreePage returns page id to the free list. The page becomes eligible for
// reuse by a future AllocatePage call.
func (p *Pager) FreePage(id PageID) error {
	if id == headerPage || int(id) >= p.numPages {
		return ErrInvalidPage
	}
	buf := make([]byte, p.pageSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(p.freeHead))
	if err := p.WritePage(id, buf); err != nil {
		return err
	}
	p.freeHead = id
	p.freeCount++
	return nil
}

// Flush writes the header page and synchronises the file to disk.
func (p *Pager) Flush() error {
	if err := p.writeHeader(); err != nil {
		return err
	}
	return p.f.Sync()
}

// Close flushes the header and closes the underlying file.
func (p *Pager) Close() error {
	if p.f == nil {
		return nil
	}
	if err := p.Flush(); err != nil {
		_ = p.f.Close()
		return err
	}
	err := p.f.Close()
	p.f = nil
	return err
}

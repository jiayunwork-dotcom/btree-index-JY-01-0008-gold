package btree

import (
	"encoding/binary"
	"errors"

	"btree-index/internal/pager"
)

// btreeMagic identifies a valid btree meta page.
const btreeMagic = 0x42545245 // "BTRE"

// ErrPageFull is returned when a node no longer fits in a single page.
var ErrPageFull = errors.New("btree: node does not fit in page")

// ErrCorrupt is returned when a page cannot be decoded into a valid node.
var ErrCorrupt = errors.New("btree: corrupt node")

// putU32 writes a little-endian uint32 and returns the new offset, or -1 if the
// value would overflow the buffer.
func putU32(b []byte, off int, v uint32) int {
	if off < 0 {
		return off
	}
	if off+4 > len(b) {
		return -1
	}
	binary.LittleEndian.PutUint32(b[off:off+4], v)
	return off + 4
}

// putBytes writes a length-prefixed byte slice and returns the new offset, or
// -1 on overflow.
func putBytes(b []byte, off int, v []byte) int {
	off = putU32(b, off, uint32(len(v)))
	if off < 0 {
		return off
	}
	if off+len(v) > len(b) {
		return -1
	}
	copy(b[off:off+len(v)], v)
	return off + len(v)
}

// getU32 reads a little-endian uint32 and returns it with the new offset, or
// (-1, -1) on underflow.
func getU32(b []byte, off int) (uint32, int) {
	if off < 0 || off+4 > len(b) {
		return 0, -1
	}
	return binary.LittleEndian.Uint32(b[off : off+4]), off + 4
}

// getBytes reads a length-prefixed byte slice and returns a detached copy.
func getBytes(b []byte, off int) ([]byte, int) {
	n, off := getU32(b, off)
	if off < 0 {
		return nil, -1
	}
	if off+int(n) > len(b) {
		return nil, -1
	}
	v := make([]byte, n)
	copy(v, b[off:off+int(n)])
	return v, off + int(n)
}

// encodeNode serialises a node into a single page-sized buffer. The first byte
// is the node type (0 = inner, 1 = leaf); the second group of four bytes is the
// key count; the remainder is the entry payload.
func encodeNode(n *node, pageSize int) ([]byte, error) {
	buf := make([]byte, pageSize)
	if n.leaf {
		buf[0] = 1
	} else {
		buf[0] = 0
	}
	off := 1
	off = putU32(buf, off, uint32(len(n.keys)))
	if off < 0 {
		return nil, ErrPageFull
	}
	if n.leaf {
		for i := range n.keys {
			off = putBytes(buf, off, n.keys[i])
			if off < 0 {
				return nil, ErrPageFull
			}
			off = putBytes(buf, off, n.vals[i])
			if off < 0 {
				return nil, ErrPageFull
			}
		}
		return buf, nil
	}
	// Inner: leading child, then (key, child) pairs.
	if len(n.kids) == 0 {
		return nil, ErrCorrupt
	}
	off = putU32(buf, off, uint32(n.kids[0]))
	if off < 0 {
		return nil, ErrPageFull
	}
	for i := range n.keys {
		off = putBytes(buf, off, n.keys[i])
		if off < 0 {
			return nil, ErrPageFull
		}
		off = putU32(buf, off, uint32(n.kids[i+1]))
		if off < 0 {
			return nil, ErrPageFull
		}
	}
	return buf, nil
}

// decodeNode reconstructs a node from its page buffer.
func decodeNode(buf []byte, self pager.PageID) (*node, error) {
	if len(buf) < 5 {
		return nil, ErrCorrupt
	}
	n := &node{self: self}
	n.leaf = buf[0] == 1
	off := 1
	cnt, off := getU32(buf, off)
	if off < 0 {
		return nil, ErrCorrupt
	}
	if n.leaf {
		n.keys = make([][]byte, cnt)
		n.vals = make([][]byte, cnt)
		for i := 0; i < int(cnt); i++ {
			var k, v []byte
			var e error
			k, off = getBytes(buf, off)
			if off < 0 {
				return nil, ErrCorrupt
			}
			v, off = getBytes(buf, off)
			if off < 0 {
				return nil, ErrCorrupt
			}
			_ = e
			n.keys[i] = k
			n.vals[i] = v
		}
		return n, nil
	}
	// Inner node.
	n.kids = make([]pager.PageID, cnt+1)
	var first uint32
	first, off = getU32(buf, off)
	if off < 0 {
		return nil, ErrCorrupt
	}
	n.kids[0] = pager.PageID(first)
	n.keys = make([][]byte, cnt)
	for i := 0; i < int(cnt); i++ {
		var k []byte
		var c uint32
		k, off = getBytes(buf, off)
		if off < 0 {
			return nil, ErrCorrupt
		}
		n.keys[i] = k
		c, off = getU32(buf, off)
		if off < 0 {
			return nil, ErrCorrupt
		}
		n.kids[i+1] = pager.PageID(c)
	}
	return n, nil
}

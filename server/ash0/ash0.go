// Package ash0 decodes Nintendo ASH0 chunks (the compression used inside Super Mario Maker course files).
//
// Format (big-endian), derived from SMMDB/ASH0/Ash.cpp and cross-checked with tools/ash0.mjs:
//
//	0x00 "ASH0"
//	0x04 u8 flags | u24 uncompressed size
//	0x08 u32 offset of bit stream 2 (from the start of the chunk)
//	0x0C bit stream 1: symbol tree (9-bit leaves, internal node ids from 0x200), then symbol codes
//	...  bit stream 2: distance tree (11-bit leaves, ids from 0x800), then one distance code per match
//
// Bits are read MSB-first from 32-bit big-endian words. Symbols 0-0xFF are literals; symbol s >= 0x100 copies
// s-0xFD bytes (3..258) from distance d+1 back.
package ash0

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// IsASH0 reports whether b looks like an ASH0 chunk.
func IsASH0(b []byte) bool {
	return len(b) > 0x10 && b[0] == 'A' && b[1] == 'S' && b[2] == 'H'
}

type bitReader struct {
	buf  []byte
	pos  int
	cur  uint32
	left int
}

func (r *bitReader) bit() int {
	if r.left == 0 {
		// * Reading past the end yields zeros (the reference decoder prefetches a word).
		if r.pos >= 0 && r.pos+4 <= len(r.buf) {
			r.cur = binary.BigEndian.Uint32(r.buf[r.pos:])
		} else {
			r.cur = 0
		}

		r.pos += 4
		r.left = 32
	}

	r.left--

	return int((r.cur >> uint(r.left)) & 1)
}

func (r *bitReader) bits(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}

	return v
}

type tree struct {
	root        int
	first       int
	left, right map[int]int
}

func readTree(r *bitReader, symBits, first int) (*tree, error) {
	t := &tree{first: first, left: map[int]int{}, right: map[int]int{}}
	next := first

	var node func(depth int) (int, error)
	node = func(depth int) (int, error) {
		if depth > 4096 {
			return 0, errors.New("ash0: tree too deep")
		}

		if r.bit() == 0 {
			return r.bits(symBits), nil // leaf
		}

		id := next
		next++

		l, err := node(depth + 1)
		if err != nil {
			return 0, err
		}

		rt, err := node(depth + 1)
		if err != nil {
			return 0, err
		}

		t.left[id], t.right[id] = l, rt

		return id, nil
	}

	root, err := node(0)
	if err != nil {
		return nil, err
	}

	t.root = root

	return t, nil
}

func (t *tree) walk(r *bitReader) (int, error) {
	n := t.root
	for n >= t.first {
		var ok bool
		if r.bit() == 1 {
			n, ok = t.right[n]
		} else {
			n, ok = t.left[n]
		}

		if !ok {
			return 0, errors.New("ash0: bad tree node")
		}
	}

	return n, nil
}

// Decompress decodes one ASH0 chunk. Trailing bytes after the chunk's streams are ignored.
func Decompress(buf []byte) ([]byte, error) {
	if !IsASH0(buf) {
		return nil, errors.New("ash0: not an ASH0 chunk")
	}

	size := int(binary.BigEndian.Uint32(buf[4:]) & 0xFFFFFF)
	off2 := int(binary.BigEndian.Uint32(buf[8:]))

	if off2 < 0x10 || off2 > len(buf) {
		return nil, fmt.Errorf("ash0: bad stream offset %#x for a %d byte chunk", off2, len(buf))
	}

	r1 := &bitReader{buf: buf, pos: 0x0C}
	r2 := &bitReader{buf: buf, pos: off2}

	t1, err := readTree(r1, 9, 0x200)
	if err != nil {
		return nil, err
	}

	t2, err := readTree(r2, 11, 0x800)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, size)
	for len(out) < size {
		s, err := t1.walk(r1)
		if err != nil {
			return nil, err
		}

		if s < 0x100 {
			out = append(out, byte(s))
			continue
		}

		d, err := t2.walk(r2)
		if err != nil {
			return nil, err
		}

		from := len(out) - (d + 1)
		if from < 0 {
			return nil, errors.New("ash0: match distance before start of output")
		}

		for k := 0; k < s-0xFD && len(out) < size; k++ {
			out = append(out, out[from+k])
		}
	}

	return out, nil
}

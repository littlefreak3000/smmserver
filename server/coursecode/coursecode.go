// Package coursecode converts between the data ID the servers use for a course and the course ID that Super Mario Maker (Wii U)
// shows in the game, such as 9007-0000-00BB-FD3B.
//
// The code is four groups of four hex digits: a checksum, 0000, and the data ID as eight hex digits (so the last eight
// digits read as hexadecimal are the data ID). The checksum is two bytes of an HMAC-MD5 of the data ID; the scheme is
// documented on the Pretendo wiki ("datastore codes") and was checked against two real codes (see the tests).
package coursecode

import (
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// accessKey is the game's data store access key; the MD5 of its text is the HMAC key.
const accessKey = "9f2b4678"

// Checksum is the first group of the code for a data ID.
func Checksum(dataID uint32) uint16 {
	key := md5.Sum([]byte(accessKey))

	var msg [8]byte
	binary.LittleEndian.PutUint64(msg[:], uint64(dataID))

	m := hmac.New(md5.New, key[:])
	m.Write(msg[:])
	sum := m.Sum(nil)

	return uint16(sum[3])<<8 | uint16(sum[2])
}

// Format is the code the game shows for a data ID, for example "9007-0000-00BB-FD3B".
func Format(dataID uint32) string {
	return fmt.Sprintf("%04X-0000-%04X-%04X", Checksum(dataID), dataID>>16, dataID&0xFFFF)
}

// Parse reads a code, with or without dashes and in any case, and returns the data ID. A code whose checksum is wrong is
// an error, as it is in the game.
func Parse(code string) (uint32, error) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	if len(s) != 16 {
		return 0, fmt.Errorf("a course ID has 16 hex digits (like 9007-0000-00BB-FD3B), %q has %d", code, len(s))
	}

	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a course ID: %w", code, err)
	}

	if (v>>32)&0xFFFF != 0 {
		return 0, fmt.Errorf("%q is not a course ID: the second group must be 0000", code)
	}

	id := uint32(v)
	if sum := uint16(v >> 48); sum != Checksum(id) {
		return 0, fmt.Errorf("%q is not a valid course ID (the first group should be %04X)", code, Checksum(id))
	}

	return id, nil
}

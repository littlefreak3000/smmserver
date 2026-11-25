// Package coursecode converts between the data ID the servers use for a course and the course ID that Super Mario Maker (Wii U)
// shows in the game, such as 9007-0000-00BB-FD3B.
//
// The code is four groups of four hex digits: a checksum and the data ID as twelve hex digits (48 bits). Every archived level
// has a data ID below 2^32, so its second group reads 0000; this server gives the levels players upload IDs from 2^32 up
// (second group 0001 and above), which no archived level can have. The checksum is two bytes of an HMAC-MD5 of the data ID;
// the scheme is documented on the Pretendo wiki ("datastore codes") and was checked against two real codes (see the tests).
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
func Checksum(dataID uint64) uint16 {
	key := md5.Sum([]byte(accessKey))

	var msg [8]byte
	binary.LittleEndian.PutUint64(msg[:], dataID)

	m := hmac.New(md5.New, key[:])
	m.Write(msg[:])
	sum := m.Sum(nil)

	return uint16(sum[3])<<8 | uint16(sum[2])
}

// MaxID is the largest data ID a course code can hold (48 bits).
const MaxID = 1<<48 - 1

// Format is the code the game shows for a data ID, for example "9007-0000-00BB-FD3B". Only the lower 48 bits are shown.
func Format(dataID uint64) string {
	dataID &= MaxID

	return fmt.Sprintf("%04X-%04X-%04X-%04X", Checksum(dataID), dataID>>32, (dataID>>16)&0xFFFF, dataID&0xFFFF)
}

// Parse reads a code, with or without dashes and in any case, and returns the data ID. A code whose checksum is wrong is
// an error, as it is in the game.
func Parse(code string) (uint64, error) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	if len(s) != 16 {
		return 0, fmt.Errorf("a course ID has 16 hex digits (like 9007-0000-00BB-FD3B), %q has %d", code, len(s))
	}

	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a course ID: %w", code, err)
	}

	id := v & MaxID
	if sum := uint16(v >> 48); sum != Checksum(id) {
		return 0, fmt.Errorf("%q is not a valid course ID (the first group should be %04X)", code, Checksum(id))
	}

	return id, nil
}

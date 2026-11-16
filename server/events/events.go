// Package events reads and writes the event course list of Super Mario Maker: the file served as data ID 900000, from
// which the game builds its "event courses" menu. See docs/event-courses.md for what is known about the format and what is
// still a guess.
//
// File layout (all numbers big-endian, all text UTF-16BE, NUL-terminated inside fixed-size windows):
//
//	u32     count
//	record  x count, each RecordSize (9,784) bytes:
//	    u64     data ID of the event course (930000 and up in the original list)
//	    12 x    title        33 characters (66 bytes)   at offset 8
//	    12 x    description  371 characters (742 bytes) at offset 800
//	    u8      1 if the event has a banner pack, else 0     at offset 9704
//	    u8      0                                             at offset 9705
//	    13 x    pack name, "Boss049.pack" style               at offset 9706 (26 bytes)
//	    u8      flag, always 1 in the original list           at offset 9732
//	    3       padding
//	    6 x u32 start: year, month, day, hour, minute, second at offset 9736
//	    6 x u32 end:   the same                               at offset 9760
//
// The 12 texts are in the order of Languages. Bytes after a text's terminator are leftovers from older edits and carry no
// meaning; Marshal writes zeros there.
package events

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
)

const (
	RecordSize = 9784

	// NumLanguages is how many titles and descriptions a record holds.
	NumLanguages = 12

	TitleUnits = 33  // UTF-16 units per title window, including the terminator
	DescUnits  = 371 // the same for a description
	PackUnits  = 13  // the same for the pack name

	titleOff = 8
	descOff  = titleOff + NumLanguages*TitleUnits*2 // 800
	packOff  = descOff + NumLanguages*DescUnits*2   // 9704
	flagOff  = packOff + 28                         // 9732
	startOff = flagOff + 4                          // 9736
	endOff   = startOff + 24                        // 9760
)

// Languages names the 12 text windows in file order. The three American and the three European entries differ in wording
// (for example "Mary O." and "Mashiko"), which is how the order was established; which one the game shows to whom is
// inferred from the names, not tested.
var Languages = [NumLanguages]string{"ja", "en-US", "fr-US", "es-US", "en-EU", "fr-EU", "es-EU", "de", "it", "nl", "ru", "pt"}

// Event is one record of the list.
type Event struct {
	ID          int64 // data ID of the course the event opens
	Title       [NumLanguages]string
	Description [NumLanguages]string
	Pack        string // name of the banner pack the game downloads through SpotPass ("Boss049.pack"), or ""
	Flag        byte   // 1 in every original record; its meaning is unknown
	Start, End  time.Time
}

// Parse decodes an event list.
func Parse(b []byte) ([]Event, error) {
	if len(b) < 4 {
		return nil, errors.New("events: the file is too short")
	}

	n := int(binary.BigEndian.Uint32(b))
	if n < 0 || len(b) != 4+n*RecordSize {
		return nil, fmt.Errorf("events: %d bytes do not hold %d records of %d bytes", len(b), n, RecordSize)
	}

	out := make([]Event, n)

	for i := range out {
		r := b[4+i*RecordSize : 4+(i+1)*RecordSize]
		e := &out[i]

		e.ID = int64(binary.BigEndian.Uint64(r))

		for k := 0; k < NumLanguages; k++ {
			e.Title[k] = text(r[titleOff+k*TitleUnits*2 : titleOff+(k+1)*TitleUnits*2])
			e.Description[k] = text(r[descOff+k*DescUnits*2 : descOff+(k+1)*DescUnits*2])
		}

		if r[packOff] != 0 {
			e.Pack = text(r[packOff+2 : packOff+2+PackUnits*2])
		}

		e.Flag = r[flagOff]
		e.Start = date(r[startOff:])
		e.End = date(r[endOff:])
	}

	return out, nil
}

// Marshal encodes an event list. Texts that do not fit their window are an error rather than being cut.
func Marshal(list []Event) ([]byte, error) {
	b := make([]byte, 4+len(list)*RecordSize)
	binary.BigEndian.PutUint32(b, uint32(len(list)))

	for i, e := range list {
		r := b[4+i*RecordSize : 4+(i+1)*RecordSize]

		if e.ID <= 0 {
			return nil, fmt.Errorf("events: event %d has no data ID", i+1)
		}

		binary.BigEndian.PutUint64(r, uint64(e.ID))

		for k := 0; k < NumLanguages; k++ {
			if err := put(r[titleOff+k*TitleUnits*2:titleOff+(k+1)*TitleUnits*2], e.Title[k]); err != nil {
				return nil, fmt.Errorf("events: event %d, title %s: %w", e.ID, Languages[k], err)
			}

			if err := put(r[descOff+k*DescUnits*2:descOff+(k+1)*DescUnits*2], e.Description[k]); err != nil {
				return nil, fmt.Errorf("events: event %d, description %s: %w", e.ID, Languages[k], err)
			}
		}

		if e.Pack != "" {
			r[packOff] = 1

			if err := put(r[packOff+2:packOff+2+PackUnits*2], e.Pack); err != nil {
				return nil, fmt.Errorf("events: event %d, pack name: %w", e.ID, err)
			}
		}

		r[flagOff] = e.Flag

		if err := putDate(r[startOff:], e.Start); err != nil {
			return nil, fmt.Errorf("events: event %d, start: %w", e.ID, err)
		}

		if err := putDate(r[endOff:], e.End); err != nil {
			return nil, fmt.Errorf("events: event %d, end: %w", e.ID, err)
		}
	}

	return b, nil
}

// text reads a NUL-terminated UTF-16BE string from a window.
func text(w []byte) string {
	u := make([]uint16, 0, len(w)/2)

	for i := 0; i+1 < len(w); i += 2 {
		c := binary.BigEndian.Uint16(w[i:])
		if c == 0 {
			break
		}

		u = append(u, c)
	}

	return string(utf16.Decode(u))
}

// put writes s into a window as UTF-16BE; the window is zeroed first and always keeps room for the terminator.
func put(w []byte, s string) error {
	u := utf16.Encode([]rune(s))
	if len(u) > len(w)/2-1 {
		return fmt.Errorf("%d characters, at most %d fit", len(u), len(w)/2-1)
	}

	for i := range w {
		w[i] = 0
	}

	for i, c := range u {
		binary.BigEndian.PutUint16(w[i*2:], c)
	}

	return nil
}

func date(b []byte) time.Time {
	f := func(i int) int { return int(binary.BigEndian.Uint32(b[i*4:])) }

	return time.Date(f(0), time.Month(f(1)), f(2), f(3), f(4), f(5), 0, time.UTC)
}

func putDate(b []byte, t time.Time) error {
	t = t.UTC()
	if t.Year() < 1 {
		return errors.New("no date given")
	}

	for i, v := range []int{t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()} {
		binary.BigEndian.PutUint32(b[i*4:], uint32(v))
	}

	return nil
}

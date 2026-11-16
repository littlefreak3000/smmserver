package events

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample() []Event {
	e := Event{ID: 930100, Pack: "Boss060.pack", Flag: 1, Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2126, 10, 1, 0, 0, 0, 0, time.UTC)}
	for k := range e.Title {
		e.Title[k] = "Haunted " + Languages[k]
		e.Description[k] = "Line one\r\nLine two " + Languages[k]
	}

	e.Title[0] = "お化け屋敷" // Japanese
	e.Description[10] = "Привет"

	plain := Event{ID: 930101, Flag: 1, Start: time.Date(2026, 12, 20, 6, 30, 15, 0, time.UTC), End: time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)}
	plain.Title[1] = "Winter"

	return []Event{e, plain}
}

func TestMarshalAndParseAreInverse(t *testing.T) {
	in := sample()

	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	if len(b) != 4+2*RecordSize || binary.BigEndian.Uint32(b) != 2 {
		t.Fatalf("size %d", len(b))
	}

	out, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip differs:\n%+v\n%+v", in, out)
	}
}

func TestOffsetsAddUp(t *testing.T) {
	if descOff != 800 || packOff != 9704 || flagOff != 9732 || startOff != 9736 || endOff != 9760 || endOff+24 != RecordSize {
		t.Fatalf("offsets: desc %d pack %d flag %d start %d end %d", descOff, packOff, flagOff, startOff, endOff)
	}
}

func TestTooLongTextsAreRefused(t *testing.T) {
	e := sample()[:1]

	e[0].Title[3] = strings.Repeat("x", TitleUnits) // one too many: the terminator needs room
	if _, err := Marshal(e); err == nil {
		t.Fatal("a 33-character title was accepted")
	}

	e[0].Title[3] = strings.Repeat("x", TitleUnits-1)
	e[0].Description[3] = strings.Repeat("y", DescUnits)

	if _, err := Marshal(e); err == nil {
		t.Fatal("a 371-character description was accepted")
	}

	e[0].Description[3] = strings.Repeat("y", DescUnits-1)
	e[0].Pack = "Boss0490.pack1"

	if _, err := Marshal(e); err == nil {
		t.Fatal("a 14-character pack name was accepted")
	}

	e[0].Pack = "Boss049.pack"
	e[0].ID = 0

	if _, err := Marshal(e); err == nil {
		t.Fatal("an event without an ID was accepted")
	}
}

func TestParseRefusesTheWrongSize(t *testing.T) {
	b, _ := Marshal(sample())

	if _, err := Parse(b[:len(b)-1]); err == nil {
		t.Fatal("a truncated list was accepted")
	}

	if _, err := Parse([]byte{0, 0}); err == nil {
		t.Fatal("a 2-byte file was accepted")
	}
}

// TestTheOriginalList checks the layout against the real file (Nintendo's, so it is not in the repository): point
// SMM_EVENT_LIST at a copy of the archived data ID 900000 file (450,068 bytes) to run it.
func TestTheOriginalList(t *testing.T) {
	path := os.Getenv("SMM_EVENT_LIST")
	if path == "" {
		t.Skip("SMM_EVENT_LIST is not set")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip(err)
	}

	list, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(list) != 46 {
		t.Fatalf("%d events, want 46", len(list))
	}

	for i, e := range list {
		if e.ID < 930000 || e.ID > 930050 || e.Flag != 1 || e.Title[1] == "" || e.Description[1] == "" || e.Title[0] == "" {
			t.Fatalf("event %d looks wrong: %+v", i, e)
		}

		if e.Start.Year() < 2015 || e.Start.Year() > 2017 || !e.End.After(e.Start.Add(-24*time.Hour*400)) {
			t.Fatalf("event %d has odd dates %v - %v", e.ID, e.Start, e.End)
		}

		if e.Pack != "" && !(strings.HasPrefix(e.Pack, "Boss") && strings.HasSuffix(e.Pack, ".pack")) {
			t.Fatalf("event %d has pack %q", e.ID, e.Pack)
		}
	}

	if list[0].ID != 930050 || list[0].Start != time.Date(2017, 10, 17, 1, 0, 0, 0, time.UTC) || list[45].Title[1] != "Ship Love" {
		t.Fatalf("first %+v, last %+v", list[0], list[45])
	}

	// * the US and the European wording differ where the originals say "Mary O." and "Mashiko"
	for _, e := range list {
		if e.ID == 930038 && (e.Title[1] != "Mary O.'s Lunch Break" || e.Title[4] != "Mashiko's Lunch Break") {
			t.Fatalf("language windows are not where expected: %q / %q", e.Title[1], e.Title[4])
		}
	}

	// * writing it back and reading it again gives the same events, and every byte that means something is the same
	again, err := Marshal(list)
	if err != nil {
		t.Fatal(err)
	}

	back, err := Parse(again)
	if err != nil || !reflect.DeepEqual(list, back) {
		t.Fatalf("round trip of the original list differs (%v)", err)
	}

	for i := range list {
		a, b := cleaned(raw[4+i*RecordSize:4+(i+1)*RecordSize]), cleaned(again[4+i*RecordSize:4+(i+1)*RecordSize])
		if !bytes.Equal(a, b) {
			t.Fatalf("record %d (%d) differs from the original outside the leftover bytes", i, list[i].ID)
		}
	}
}

// cleaned zeroes the leftover bytes after a text's terminator, which carry no meaning.
func cleaned(r []byte) []byte {
	out := append([]byte(nil), r...)

	wipe := func(off, units, count int) {
		for k := 0; k < count; k++ {
			w := out[off+k*units*2 : off+(k+1)*units*2]
			end := len(w)

			for i := 0; i+1 < len(w); i += 2 {
				if w[i] == 0 && w[i+1] == 0 {
					end = i

					break
				}
			}

			for i := end; i < len(w); i++ {
				w[i] = 0
			}
		}
	}

	wipe(titleOff, TitleUnits, NumLanguages)
	wipe(descOff, DescUnits, NumLanguages)

	// * the pack name only counts when the record says it has one
	if out[packOff] == 0 {
		for i := packOff + 1; i < flagOff; i++ {
			out[i] = 0
		}
	} else {
		wipe(packOff+2, PackUnits, 1)
	}

	for i := flagOff + 1; i < startOff; i++ { // padding
		out[i] = 0
	}

	return out
}

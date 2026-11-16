package coursecode

import "testing"

// Two real codes: the first is the example on the Pretendo wiki, the second was shown by the game for a level the server
// served as data ID 12320059 (Cemu, 2026-11-15).
var known = []struct {
	id   uint32
	code string
}{
	{4005199, "77A9-0000-003D-1D4F"},
	{12320059, "9007-0000-00BB-FD3B"},
}

func TestKnownCodes(t *testing.T) {
	for _, k := range known {
		if got := Format(k.id); got != k.code {
			t.Errorf("Format(%d) = %s, want %s", k.id, got, k.code)
		}

		id, err := Parse(k.code)
		if err != nil || id != k.id {
			t.Errorf("Parse(%s) = %d, %v, want %d", k.code, id, err, k.id)
		}
	}
}

func TestParseAcceptsOtherSpellings(t *testing.T) {
	for _, in := range []string{"9007000000BBFD3B", "9007-0000-00bb-fd3b", " 9007 0000 00BB FD3B ", "9007-0000-00BB-FD3B"} {
		if id, err := Parse(in); err != nil || id != 12320059 {
			t.Errorf("Parse(%q) = %d, %v", in, id, err)
		}
	}
}

func TestParseRefusesWrongCodes(t *testing.T) {
	for _, in := range []string{
		"9008-0000-00BB-FD3B", // wrong checksum
		"9007-0001-00BB-FD3B", // second group not zero
		"9007-0000-00BB-FD3",  // too short
		"9007-0000-00BB-FD3BB",
		"ZZZZ-0000-00BB-FD3B",
		"",
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) was accepted", in)
		}
	}
}

func TestEveryEventCourseRoundTrips(t *testing.T) {
	for id := uint32(930000); id <= 930050; id++ {
		if got, err := Parse(Format(id)); err != nil || got != id {
			t.Fatalf("%d -> %s -> %d, %v", id, Format(id), got, err)
		}
	}
}

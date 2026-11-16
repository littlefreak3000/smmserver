package events

import (
	"strings"
	"testing"
	"time"
)

func TestTheExampleBuilds(t *testing.T) {
	list, err := FromJSON([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}

	if len(list) != 2 || list[0].ID != 930100 || list[0].Title[0] != "おばけやしき" || list[0].Title[1] != "Haunted Castle" {
		t.Fatalf("%+v", list)
	}

	if !strings.Contains(list[0].Description[3], "\r\n") || list[0].Flag != 1 {
		t.Fatalf("description %q, flag %d", list[0].Description[3], list[0].Flag)
	}

	if want := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC); !list[0].End.Equal(want) {
		t.Fatalf("end %v, want %v", list[0].End, want)
	}

	// * no end date: a hundred years after the start, as in the original list
	if want := time.Date(2126, 12, 20, 0, 0, 0, 0, time.UTC); !list[1].End.Equal(want) {
		t.Fatalf("default end %v, want %v", list[1].End, want)
	}

	b, err := Marshal(list)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Parse(b); err != nil {
		t.Fatal(err)
	}
}

func TestBadSpecsAreRefused(t *testing.T) {
	for name, in := range map[string]string{
		"no events":         `{"events":[]}`,
		"unknown key":       `{"events":[{"id":1,"titel":"x","start":"2026-01-01"}]}`,
		"unknown language":  `{"events":[{"id":1,"titles":{"xx":"x"},"start":"2026-01-01"}]}`,
		"missing start":     `{"events":[{"id":1,"title":"x"}]}`,
		"not a date":        `{"events":[{"id":1,"title":"x","start":"yesterday"}]}`,
		"not json":          `events`,
		"description lang":  `{"events":[{"id":1,"descriptions":{"zz":"x"},"start":"2026-01-01"}]}`,
		"end is not a date": `{"events":[{"id":1,"title":"x","start":"2026-01-01","end":"soon"}]}`,
	} {
		if _, err := FromJSON([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// * a title that is too long is caught when writing, not silently cut
	list, err := FromJSON([]byte(`{"events":[{"id":1,"title":"` + strings.Repeat("x", 40) + `","start":"2026-01-01"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Marshal(list); err == nil {
		t.Fatal("a 40-character title was written")
	}
}

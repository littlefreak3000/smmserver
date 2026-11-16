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

	// * three menu entries: one course for the first event, two for the second
	if len(list) != 3 || list[0].ID != 12320059 || list[0].Title[0] != "おばけやしき" || list[0].Title[1] != "Haunted Castle" {
		t.Fatalf("%+v", list)
	}

	if list[1].ID != 4005199 || list[2].ID != 6563372 || list[1].Title[1] != "Winter Wonderland 1/2" || list[2].Title[1] != "Winter Wonderland 2/2" || list[1].Title[0] != "Winter Wonderland 1/2" {
		t.Fatalf("a list of courses did not become one entry each: %q %q", list[1].Title[1], list[2].Title[1])
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

func TestACourseCanBeADataIDOrTheCodeTheGameShows(t *testing.T) {
	for _, id := range []string{`12320059`, `"12320059"`, `"9007-0000-00BB-FD3B"`, `"9007000000bbfd3b"`} {
		list, err := FromJSON([]byte(`{"events":[{"course":` + id + `,"title":"x","start":"2026-01-01"}]}`))
		if err != nil || list[0].ID != 12320059 {
			t.Errorf("id %s: %v, %+v", id, err, list)
		}
	}

	for _, id := range []string{`"9008-0000-00BB-FD3B"`, `"hello"`, `0`, `-5`} {
		if _, err := FromJSON([]byte(`{"events":[{"course":` + id + `,"title":"x","start":"2026-01-01"}]}`)); err == nil {
			t.Errorf("id %s was accepted", id)
		}
	}
}

func TestCourseOrCoursesButNotBothAndNoDuplicates(t *testing.T) {
	for name, in := range map[string]string{
		"neither":    `{"events":[{"title":"x","start":"2026-01-01"}]}`,
		"both":       `{"events":[{"course":1,"courses":[2],"title":"x","start":"2026-01-01"}]}`,
		"twice":      `{"events":[{"courses":[5,6,5],"title":"x","start":"2026-01-01"}]}`,
		"two events": `{"events":[{"course":5,"title":"x","start":"2026-01-01"},{"course":5,"title":"y","start":"2026-01-01"}]}`,
		"empty list": `{"events":[{"courses":[],"title":"x","start":"2026-01-01"}]}`,
	} {
		if _, err := FromJSON([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// * a title with no room for the "1/2" suffix is refused when writing
	list, err := FromJSON([]byte(`{"events":[{"courses":[5,6],"title":"` + strings.Repeat("x", 31) + `","start":"2026-01-01"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Marshal(list); err == nil {
		t.Fatal("a title that no longer fits after numbering was written")
	}
}

func TestBadSpecsAreRefused(t *testing.T) {
	for name, in := range map[string]string{
		"no events":         `{"events":[]}`,
		"unknown key":       `{"events":[{"course":1,"titel":"x","start":"2026-01-01"}]}`,
		"unknown language":  `{"events":[{"course":1,"titles":{"xx":"x"},"start":"2026-01-01"}]}`,
		"missing start":     `{"events":[{"course":1,"title":"x"}]}`,
		"not a date":        `{"events":[{"course":1,"title":"x","start":"yesterday"}]}`,
		"not json":          `events`,
		"description lang":  `{"events":[{"course":1,"descriptions":{"zz":"x"},"start":"2026-01-01"}]}`,
		"end is not a date": `{"events":[{"course":1,"title":"x","start":"2026-01-01","end":"soon"}]}`,
	} {
		if _, err := FromJSON([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// * a title that is too long is caught when writing, not silently cut
	list, err := FromJSON([]byte(`{"events":[{"course":1,"title":"` + strings.Repeat("x", 40) + `","start":"2026-01-01"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Marshal(list); err == nil {
		t.Fatal("a 40-character title was written")
	}
}

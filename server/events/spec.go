package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Example is a small events.json that FromJSON accepts.
const Example = `{
  "events": [
    {
      "id": 930100,
      "title": "Haunted Castle",
      "description": "A spooky course for the Halloween week.\r\nGood luck!",
      "titles": { "ja": "おばけやしき" },
      "start": "2026-10-25",
      "end": "2026-11-02"
    },
    {
      "id": 930101,
      "title": "Winter Wonderland",
      "description": "Snow, ice and presents.",
      "start": "2026-12-20"
    }
  ]
}
`

type spec struct {
	Events []specEvent `json:"events"`
}

type specEvent struct {
	ID           int64             `json:"id"`
	Title        string            `json:"title"`        // used for every language without an entry in titles
	Description  string            `json:"description"`  // the same for descriptions
	Titles       map[string]string `json:"titles"`       // by language code (see Languages)
	Descriptions map[string]string `json:"descriptions"` // by language code
	Pack         string            `json:"pack"`
	Start        string            `json:"start"`
	End          string            `json:"end"` // default: a hundred years after the start, like the original events
	Flag         *int              `json:"flag"`
}

// FromJSON turns an events.json file into event records. Unknown fields are an error, so a misspelt key is not ignored.
func FromJSON(raw []byte) ([]Event, error) {
	var f spec

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("events.json: %w", err)
	}

	if len(f.Events) == 0 {
		return nil, fmt.Errorf("events.json has no events")
	}

	lang := map[string]int{}
	for i, l := range Languages {
		lang[l] = i
	}

	out := make([]Event, 0, len(f.Events))

	for n, j := range f.Events {
		e := Event{ID: j.ID, Pack: j.Pack, Flag: 1}

		if j.Flag != nil {
			e.Flag = byte(*j.Flag)
		}

		for k := range e.Title {
			e.Title[k], e.Description[k] = j.Title, j.Description
		}

		for code, s := range j.Titles {
			k, ok := lang[code]
			if !ok {
				return nil, fmt.Errorf("event %d: %q is not a language (use %s)", n+1, code, strings.Join(Languages[:], ", "))
			}

			e.Title[k] = s
		}

		for code, s := range j.Descriptions {
			k, ok := lang[code]
			if !ok {
				return nil, fmt.Errorf("event %d: %q is not a language (use %s)", n+1, code, strings.Join(Languages[:], ", "))
			}

			e.Description[k] = s
		}

		var err error

		if e.Start, err = parseDate(j.Start); err != nil {
			return nil, fmt.Errorf("event %d: start: %w", n+1, err)
		}

		if j.End == "" {
			e.End = e.Start.AddDate(100, 0, 0)
		} else if e.End, err = parseDate(j.End); err != nil {
			return nil, fmt.Errorf("event %d: end: %w", n+1, err)
		}

		out = append(out, e)
	}

	return out, nil
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("a date is needed (2026-10-25 or 2026-10-25T18:00:00Z)")
	}

	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("%q is not a date (2026-10-25 or 2026-10-25T18:00:00Z)", s)
}

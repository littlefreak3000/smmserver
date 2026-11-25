package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/coursecode"
)

// Example is a small events.json that FromJSON accepts. Courses are written the way the game shows them.
const Example = `{
  "events": [
    {
      "course": "9007-0000-00BB-FD3B",
      "title": "Haunted Castle",
      "description": "A spooky course for the Halloween week.\r\nGood luck!",
      "titles": { "ja": "おばけやしき" },
      "start": "2026-10-25",
      "end": "2026-11-02"
    },
    {
      "courses": ["77A9-0000-003D-1D4F", "AC6F-0000-0064-262C"],
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

// courseRef is a course in events.json: the course ID the game shows ("9007-0000-00BB-FD3B"), or a data ID (12320059).
type courseRef int64

func (c *courseRef) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	s = strings.Trim(s, `"`)

	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 && len(s) < 16 {
		*c = courseRef(n)

		return nil
	}

	id, err := coursecode.Parse(s)
	if err != nil {
		return fmt.Errorf("a course is a data ID or a course ID like 9007-0000-00BB-FD3B: %w", err)
	}

	*c = courseRef(id)

	return nil
}

type specEvent struct {
	Course       courseRef         `json:"course"`       // the course the event opens, as the game shows it
	Courses      []courseRef       `json:"courses"`      // several courses: one menu entry each, titled "title 1/3", "title 2/3", ...
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

	seen := map[int64]bool{}

	for n, j := range f.Events {
		ids := make([]int64, 0, 1+len(j.Courses))

		if j.Course != 0 {
			ids = append(ids, int64(j.Course))
		}

		for _, c := range j.Courses {
			ids = append(ids, int64(c))
		}

		if (j.Course != 0) == (len(j.Courses) != 0) {
			return nil, fmt.Errorf("event %d: give either \"course\" (one course) or \"courses\" (a list)", n+1)
		}

		base := Event{Pack: j.Pack, Flag: 1}

		if j.Flag != nil {
			base.Flag = byte(*j.Flag)
		}

		for k := range base.Title {
			base.Title[k], base.Description[k] = j.Title, j.Description
		}

		for code, s := range j.Titles {
			k, ok := lang[code]
			if !ok {
				return nil, fmt.Errorf("event %d: %q is not a language (use %s)", n+1, code, strings.Join(Languages[:], ", "))
			}

			base.Title[k] = s
		}

		for code, s := range j.Descriptions {
			k, ok := lang[code]
			if !ok {
				return nil, fmt.Errorf("event %d: %q is not a language (use %s)", n+1, code, strings.Join(Languages[:], ", "))
			}

			base.Description[k] = s
		}

		var err error

		if base.Start, err = parseDate(j.Start); err != nil {
			return nil, fmt.Errorf("event %d: start: %w", n+1, err)
		}

		if j.End == "" {
			base.End = base.Start.AddDate(100, 0, 0)
		} else if base.End, err = parseDate(j.End); err != nil {
			return nil, fmt.Errorf("event %d: end: %w", n+1, err)
		}

		// * the menu opens one course per entry, so a list becomes one entry per course
		for i, id := range ids {
			if seen[id] {
				return nil, fmt.Errorf("event %d: course %s is listed twice", n+1, coursecode.Format(uint64(id)))
			}

			seen[id] = true

			e := base
			e.ID = id

			if len(ids) > 1 {
				for k := range e.Title {
					if e.Title[k] != "" {
						e.Title[k] = fmt.Sprintf("%s %d/%d", e.Title[k], i+1, len(ids))
					}
				}
			}

			out = append(out, e)
		}
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

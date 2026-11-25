package webui

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Setting is one entry of settings.env as the web page shows it. The list is read out of the commented template the
// server writes on its first start, so the help text on the page is the help text in the file and the two cannot drift.
type Setting struct {
	Key     string `json:"key"`
	Group   string `json:"group"`
	Help    string `json:"help"`
	Default string `json:"default"`
	Kind    string `json:"kind"` // toggle (0/1), int, duration or text
}

var settingLine = regexp.MustCompile(`^#?\s*([A-Z][A-Z0-9_]*)=(.*)$`)

var kinds = map[string]string{
	"SMM_FETCH": "toggle", "SMM_NARROW_NORMAL": "toggle", "SMM_PROXY_PRETENDO": "toggle", "SMM_TRACE": "toggle",
	"SMM_POOL_TARGET": "int", "SMM_POOL_GROW_BY": "int", "SMM_POOL_MAX": "int", "SMM_CACHE_MAX_MB": "int",
	"SMM_DIFFICULTY_CEILING": "int", "SMM_ACCOUNT_PORT": "int", "SMM_S3_PORT": "int", "SMM_PROXY_PORT": "int",
	"PN_SMM_AUTHENTICATION_SERVER_PORT": "int", "PN_SMM_SECURE_SERVER_PORT": "int", "SMM_WEB_PORT": "int",
	"SMM_LIST_WAIT": "int", "SMM_POOL_GROW_EVERY": "duration",
}

// exampleOnly are settings whose line in the template shows an example value, not the default.
var exampleOnly = map[string]bool{"SMM_PUBLIC_HOST": true}

var groups = []struct{ Name, Prefix string }{
	{"Levels and difficulty", "SMM_FETCH SMM_POOL SMM_DIFFICULTY SMM_NARROW SMM_HISTORY SMM_CACHE SMM_INDEX SMM_LIST"},
	{"Network", "SMM_PUBLIC SMM_ACCOUNT SMM_S3 SMM_PROXY PN_SMM SMM_WEB"},
}

func groupOf(key string) string {
	for _, g := range groups {
		for _, p := range strings.Fields(g.Prefix) {
			if strings.HasPrefix(key, p) {
				return g.Name
			}
		}
	}

	return "Other"
}

// ParseTemplate turns a commented settings template into a list of settings. Comment lines directly above a setting are its
// help; a block of comments above several settings in a row is the help of each of them.
func ParseTemplate(template string) []Setting {
	var (
		out     []Setting
		pending []string
		used    bool
	)

	for _, raw := range strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)

		if m := settingLine.FindStringSubmatch(line); m != nil {
			kind := kinds[m[1]]
			if kind == "" {
				kind = "text"
			}

			def := strings.TrimSpace(m[2])
			if exampleOnly[m[1]] {
				def = "" // * the template line is an example, the real default is worked out by the server
			}

			out = append(out, Setting{
				Key: m[1], Group: groupOf(m[1]), Default: def, Kind: kind,
				Help: strings.Join(pending, " "),
			})
			used = true

			continue
		}

		switch {
		case strings.HasPrefix(line, "#"):
			if used {
				pending, used = nil, false
			}

			pending = append(pending, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		case line == "":
			pending, used = nil, false
		}
	}

	// * the file header ("Settings for the ...") is not the help of the first setting
	for i := range out {
		if strings.HasPrefix(out[i].Help, "Settings for the ") {
			out[i].Help = ""
		}
	}

	return out
}

// Validate checks a value for a setting. extra, when it knows the key, has the last word (used for the settings only the
// server itself can judge, like the difficulty bands).
func Validate(s Setting, value string, extra func(key, value string) error) error {
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("a setting is one line")
	}

	if value == "" {
		return nil
	}

	switch s.Kind {
	case "toggle":
		if value != "0" && value != "1" {
			return errors.New("must be 0 or 1")
		}
	case "int":
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return errors.New("must be a whole number, 0 or more")
		}
	case "duration":
		if value != "0" {
			if d, err := time.ParseDuration(value); err != nil || d < 0 {
				return errors.New("must be a time such as 10m or 1h (or 0)")
			}
		}
	}

	if extra != nil {
		return extra(s.Key, value)
	}

	return nil
}

// File edits settings.env in place: values are changed on their own lines, comments stay.
type File struct {
	Path     string
	Template string // written when the file does not exist yet
}

// Values returns the settings that are switched on in the file (uncommented lines).
func (f File) Values() (map[string]string, error) {
	text, err := f.read()
	if err != nil {
		return nil, err
	}

	vals := map[string]string{}

	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}

		if m := settingLine.FindStringSubmatch(line); m != nil {
			vals[m[1]] = strings.TrimSpace(m[2])
		}
	}

	return vals, nil
}

func (f File) read() (string, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f.Template, nil
	}

	return string(b), err
}

// Apply sets (value != "") or resets (value == "", the line is commented out again) the given settings and writes the file.
// Every key must be one of known; nothing is written if any value is refused.
func (f File) Apply(changes map[string]string, known []Setting, extra func(key, value string) error) error {
	byKey := map[string]Setting{}
	for _, s := range known {
		byKey[s.Key] = s
	}

	for k, v := range changes {
		s, ok := byKey[k]
		if !ok {
			return fmt.Errorf("%s is not a setting", k)
		}

		if err := Validate(s, strings.TrimSpace(v), extra); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}

	text, err := f.read()
	if err != nil {
		return err
	}

	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	for k, v := range changes {
		v = strings.TrimSpace(v)
		replaced := false

		for i, line := range lines {
			m := settingLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || m[1] != k {
				continue
			}

			// * a commented example keeps the setting's place; an active line is replaced
			switch {
			case v != "":
				lines[i] = k + "=" + v
			case strings.HasPrefix(strings.TrimSpace(line), "#"):
				// already off
			default:
				lines[i] = "#" + k + "=" + byKey[k].Default
			}

			replaced = true

			break
		}

		if !replaced && v != "" {
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}

			lines = append(lines, "", "# Set from the web page.", k+"="+v, "")
		}
	}

	out := strings.Join(lines, nl)

	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}

	if err := os.Rename(tmp, f.Path); err != nil {
		os.Remove(tmp)
		return err
	}

	return nil
}

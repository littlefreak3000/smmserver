package webui

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

const template = `# Settings for the test. Remove the leading # from a line to change it.
# Real environment variables win.

# Fetch levels. 0 = offline.
#SMM_FETCH=1
# Levels per band.
#SMM_POOL_TARGET=1000

# Ports. Allow these in the firewall.
#SMM_ACCOUNT_PORT=8080
#SMM_S3_PORT=9000
`

func TestParseTemplateReadsKeysDefaultsAndHelp(t *testing.T) {
	got := ParseTemplate(template)
	if len(got) != 4 {
		t.Fatalf("got %d settings, want 4: %+v", len(got), got)
	}

	if got[0].Key != "SMM_FETCH" || got[0].Default != "1" || got[0].Kind != "toggle" || got[0].Help != "Fetch levels. 0 = offline." {
		t.Fatalf("first setting: %+v", got[0])
	}

	if got[1].Kind != "int" || got[1].Help != "Levels per band." {
		t.Fatalf("second setting: %+v", got[1])
	}

	// * a block of comments above several settings is the help of each
	if got[2].Help != got[3].Help || !strings.HasPrefix(got[2].Help, "Ports.") {
		t.Fatalf("port help: %q / %q", got[2].Help, got[3].Help)
	}

	if got[0].Group == "" {
		t.Fatal("no group")
	}
}

func TestTheRealTemplateParses(t *testing.T) {
	// * the template the server really writes: every setting has a help text and none is lost
	b, err := os.ReadFile("../local.go")
	if err != nil {
		t.Skip("local.go not found")
	}

	s := string(b)
	i := strings.Index(s, "const settingsTemplate = `")
	j := strings.Index(s[i+26:], "`")
	got := ParseTemplate(s[i+26 : i+26+j])

	for _, st := range got {
		if st.Help == "" {
			t.Errorf("%s has no help", st.Key)
		}
	}

	for _, want := range []string{"SMM_POOL_TARGET", "SMM_POOL_BANDS", "SMM_PROXY_PRETENDO", "SMM_WEB_PORT", "SMM_DIFFICULTY_CEILING"} {
		found := false

		for _, st := range got {
			found = found || st.Key == want
		}

		if !found {
			t.Errorf("%s missing from the parsed template", want)
		}
	}
}

func newFile(t *testing.T) File {
	t.Helper()

	return File{Path: filepath.Join(t.TempDir(), "settings.env"), Template: template}
}

func TestApplySetsAndResetsAndKeepsComments(t *testing.T) {
	f, known := newFile(t), ParseTemplate(template)

	if err := f.Apply(map[string]string{"SMM_POOL_TARGET": "500", "SMM_FETCH": "0"}, known, nil); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(f.Path)
	text := string(b)

	for _, want := range []string{"\nSMM_POOL_TARGET=500\n", "\nSMM_FETCH=0\n", "# Levels per band.", "#SMM_S3_PORT=9000"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	vals, _ := f.Values()
	if vals["SMM_POOL_TARGET"] != "500" || vals["SMM_FETCH"] != "0" || len(vals) != 2 {
		t.Fatalf("values: %v", vals)
	}

	// * emptying a value puts the commented default back
	if err := f.Apply(map[string]string{"SMM_POOL_TARGET": ""}, known, nil); err != nil {
		t.Fatal(err)
	}

	b, _ = os.ReadFile(f.Path)
	if !strings.Contains(string(b), "\n#SMM_POOL_TARGET=1000\n") {
		t.Fatalf("not reset:\n%s", b)
	}
}

func TestApplyAddsASettingTheFileDoesNotHaveYet(t *testing.T) {
	f, known := newFile(t), ParseTemplate(template)
	// * a file from an older version has no line for the setting
	os.WriteFile(f.Path, []byte("#SMM_FETCH=1\r\n"), 0o644)

	if err := f.Apply(map[string]string{"SMM_S3_PORT": "9100"}, known, nil); err != nil {
		t.Fatal(err)
	}

	vals, _ := f.Values()
	if vals["SMM_S3_PORT"] != "9100" {
		t.Fatalf("values: %v", vals)
	}

	b, _ := os.ReadFile(f.Path)
	if !strings.Contains(string(b), "\r\n") || strings.Contains(strings.ReplaceAll(string(b), "\r\n", ""), "\n") {
		t.Fatalf("line endings were mixed: %q", b)
	}
}

func TestApplyRefusesBadValuesAndUnknownKeysAndWritesNothing(t *testing.T) {
	f, known := newFile(t), ParseTemplate(template)

	for name, changes := range map[string]map[string]string{
		"unknown key":    {"SMM_NOPE": "1"},
		"toggle":         {"SMM_FETCH": "yes"},
		"number":         {"SMM_POOL_TARGET": "-5"},
		"second line":    {"SMM_POOL_TARGET": "5\nSMM_FETCH=0"},
		"valid + broken": {"SMM_S3_PORT": "9100", "SMM_POOL_TARGET": "x"},
	} {
		if err := f.Apply(changes, known, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := os.Stat(f.Path); err == nil {
		t.Fatal("the file was written although every change was refused")
	}

	// * the server's own judgement is asked too
	err := f.Apply(map[string]string{"SMM_S3_PORT": "9100"}, known, func(k, v string) error { return os.ErrInvalid })
	if err == nil {
		t.Fatal("the extra validator was ignored")
	}
}

// course builds a decompressed course blob with the given objects (x, y in tiles).
func course(theme byte, mode string, objs ...[]int) []byte {
	b := make([]byte, 0x15000)
	copy(b[0x6A:], mode)
	b[0x6D] = theme
	binary.BigEndian.PutUint16(b[0x70:], 300)
	binary.BigEndian.PutUint32(b[0x74:], 240*16)
	binary.BigEndian.PutUint32(b[0xEC:], uint32(len(objs)))

	for i, c := range utf16.Encode([]rune("Test")) {
		binary.BigEndian.PutUint16(b[0x28+2*i:], c)
	}

	for i, o := range objs {
		p := headerSize + objectSize*i
		binary.BigEndian.PutUint32(b[p:], uint32(o[0]*160))
		binary.BigEndian.PutUint16(b[p+8:], uint16(int16(o[1]*160)))
		b[p+0x18] = byte(o[2])
	}

	return b
}

func TestParseCourse(t *testing.T) {
	c, err := ParseCourse(course(2, "M3", []int{5, 1, 7}, []int{10, 2, 0}, []int{12, 3, 30}))
	if err != nil {
		t.Fatal(err)
	}

	if c.Name != "Test" || c.Mode != "M3" || c.ThemeName != "castle" || c.TimeLimit != 300 || c.WidthBlocks != 240 || len(c.Objects) != 3 {
		t.Fatalf("header: %+v", c)
	}

	if o := c.Objects[0]; o.X != 5 || o.Y != 1 || o.Type != 7 || !o.Block || o.Name != "Ground" {
		t.Fatalf("object 0: %+v", o)
	}

	if o := c.Objects[1]; o.Block || o.Name != "Goomba" || o.Size != 1 {
		t.Fatalf("object 1: %+v", o)
	}

	// * a Lakitu is drawn one tile higher than its stored position
	if o := c.Objects[2]; o.Y != 4 {
		t.Fatalf("lakitu y %d, want 4", o.Y)
	}
}

func TestParseCourseRefusesDamagedData(t *testing.T) {
	if _, err := ParseCourse(make([]byte, 10)); err == nil {
		t.Fatal("a tiny blob was accepted")
	}

	b := course(0, "M1")
	binary.BigEndian.PutUint32(b[0xEC:], 5000)

	if _, err := ParseCourse(b); err == nil {
		t.Fatal("an absurd object count was accepted")
	}
}

func TestGuardRefusesOtherSitesAndNonJSONPosts(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	do := func(method, host, ctype, origin string) int {
		r := httptest.NewRequest(method, "http://"+host+"/api/settings", nil)
		r.Host = host

		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}

		if origin != "" {
			r.Header.Set("Origin", origin)
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		return w.Code
	}

	for name, c := range map[string]struct {
		code                        int
		method, host, ctype, origin string
	}{
		"localhost get":   {200, "GET", "localhost:8081", "", ""},
		"ip get":          {200, "GET", "192.168.1.12:8081", "", ""},
		"ipv6 get":        {200, "GET", "[::1]:8081", "", ""},
		"rebinding name":  {403, "GET", "evil.example.com:8081", "", ""},
		"json post":       {200, "POST", "localhost:8081", "application/json", "http://localhost:8081"},
		"form post":       {415, "POST", "localhost:8081", "application/x-www-form-urlencoded", ""},
		"cross-site json": {403, "POST", "localhost:8081", "application/json", "http://evil.example.com"},
	} {
		if got := do(c.method, c.host, c.ctype, c.origin); got != c.code {
			t.Errorf("%s: %d, want %d", name, got, c.code)
		}
	}
}

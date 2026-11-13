package challenge_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/challenge"
	"github.com/PretendoNetwork/super-mario-maker/database"
)

func newStore(t *testing.T) *challenge.Store {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	return &challenge.Store{DB: db}
}

func TestPlayerSeedBeatsEveryoneSeedAndNNIDsIgnoreCase(t *testing.T) {
	s := newStore(t)

	if got := s.Effective("Alice"); got != "" {
		t.Fatalf("no seed was set, got %q", got)
	}

	if err := s.Set(challenge.Everyone, "  Friday   Race "); err != nil {
		t.Fatal(err)
	}

	if got := s.Effective("Alice"); got != "Friday Race" {
		t.Fatalf("everyone's seed = %q (spacing is tidied, spelling kept)", got)
	}

	if err := s.Set(challenge.PlayerScope("Alice"), "my own"); err != nil {
		t.Fatal(err)
	}

	if s.Effective("alice") != "my own" || s.Effective("ALICE") != "my own" {
		t.Fatal("NNIDs are not case sensitive")
	}

	if s.Effective("Bob") != "Friday Race" {
		t.Fatal("other players keep the shared seed")
	}

	// * replacing, then clearing the player's seed falls back to the shared one
	if err := s.Set(challenge.PlayerScope("Alice"), "again"); err != nil || s.Effective("Alice") != "again" {
		t.Fatalf("replace failed: %v %q", err, s.Effective("Alice"))
	}

	if err := s.Clear(challenge.PlayerScope("Alice")); err != nil || s.Effective("Alice") != "Friday Race" {
		t.Fatalf("clear failed: %v %q", err, s.Effective("Alice"))
	}

	if err := s.Clear(challenge.Everyone); err != nil || s.Effective("Alice") != "" {
		t.Fatalf("clear everyone failed: %v", err)
	}

	list, _ := s.List()
	if len(list) != 0 {
		t.Fatalf("seeds left: %+v", list)
	}

	if s.Effective("") != "" { // an unknown player (no NNID) gets only the shared seed, never an "nnid:" entry
		t.Fatal("empty NNID matched something")
	}
}

func TestValidation(t *testing.T) {
	s := newStore(t)

	for _, seed := range []string{"", "   ", strings.Repeat("x", 65), "tab\there\x00"} {
		if err := s.Set(challenge.Everyone, seed); err == nil {
			t.Fatalf("accepted bad seed %q", seed)
		}
	}

	if err := s.Set(challenge.Everyone, strings.Repeat("ミ", 64)); err != nil {
		t.Fatalf("64 characters (not bytes) should be allowed: %v", err)
	}

	for _, scope := range []string{"", "nnid:", "nnid:has space", "everyone", "nnid:" + strings.Repeat("a", 65)} {
		if err := s.Set(scope, "ok"); err == nil {
			t.Fatalf("accepted bad scope %q", scope)
		}

		if err := s.Clear(scope); err == nil {
			t.Fatalf("clear accepted bad scope %q", scope)
		}
	}
}

func TestRandomSeedsAreShortReadableAndDifferent(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 200; i++ {
		s := challenge.RandomSeed()
		if !regexp.MustCompile(`^[a-z2-9]{6}$`).MatchString(s) || strings.ContainsAny(s, "lio01") {
			t.Fatalf("bad random seed %q", s)
		}

		seen[s] = true
	}

	if len(seen) < 190 {
		t.Fatalf("only %d distinct seeds in 200", len(seen))
	}
}

func TestChallengeCodeIgnoresCaseAndSpacing(t *testing.T) {
	if a, b := challenge.Code("Friday  Race", "66FBEDB2"), challenge.Code("friday race", "66FBEDB2"); a != b || a != "friday race-66FBEDB2" {
		t.Fatalf("%q %q", a, b)
	}
}

func serve(t *testing.T, s *challenge.Store, info challenge.Info) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(s.Handler(func() challenge.Info { return info }))
	t.Cleanup(srv.Close)

	return srv
}

func post(t *testing.T, srv *httptest.Server, form url.Values) (int, string) {
	t.Helper()

	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := c.PostForm(srv.URL+"/seed", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()

	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

func TestSeedPageSetRandomClearAndJSON(t *testing.T) {
	s := newStore(t)
	srv := serve(t, s, challenge.Info{Mode: "the whole archive (404 levels)", Fingerprint: "66FBEDB2", Players: []string{"Alice", "Bob_2"}})

	code, body := get(t, srv, "/seed")
	if code != 200 || !strings.Contains(body, "the whole archive (404 levels)") || !strings.Contains(body, `value="nnid:Bob_2"`) || !strings.Contains(body, "None: levels are random") {
		t.Fatalf("page: %d\n%s", code, body)
	}

	if code, _ := post(t, srv, url.Values{"action": {"set"}, "who": {"*"}, "seed": {"Friday Race"}}); code != http.StatusSeeOther {
		t.Fatalf("set answered %d", code)
	}

	if code, _ := post(t, srv, url.Values{"action": {"set"}, "who": {"nnid:Alice"}, "seed": {"mine"}}); code != http.StatusSeeOther {
		t.Fatalf("set (player) answered %d", code)
	}

	if s.Effective("Bob_2") != "Friday Race" || s.Effective("Alice") != "mine" {
		t.Fatal("the page did not store the seeds")
	}

	_, body = get(t, srv, "/seed")
	if !strings.Contains(body, "<code>friday race-66FBEDB2</code>") || !strings.Contains(body, "<code>mine-66FBEDB2</code>") {
		t.Fatalf("challenge codes missing from the page:\n%s", body)
	}

	// * JSON view
	_, js := get(t, srv, "/seed.json")

	var out struct {
		Fingerprint string
		Seeds       []struct{ Scope, Seed, Code string }
	}

	if err := json.Unmarshal([]byte(js), &out); err != nil || out.Fingerprint != "66FBEDB2" || len(out.Seeds) != 2 || out.Seeds[0].Scope != "*" || out.Seeds[0].Code != "friday race-66FBEDB2" {
		t.Fatalf("json: %v %+v", err, out)
	}

	// * random, then clear
	post(t, srv, url.Values{"action": {"random"}, "who": {"nnid:Bob_2"}})

	if got := s.Effective("Bob_2"); !regexp.MustCompile(`^[a-z2-9]{6}$`).MatchString(got) {
		t.Fatalf("random seed for Bob = %q", got)
	}

	post(t, srv, url.Values{"action": {"clear"}, "who": {"nnid:Bob_2"}})
	post(t, srv, url.Values{"action": {"clear"}, "who": {"*"}})

	if s.Effective("Bob_2") != "" || s.Effective("Carol") != "" {
		t.Fatal("clearing did not work")
	}

	if _, js := get(t, srv, "/seed.json"); !strings.Contains(js, `"seeds":[`) || strings.Contains(js, "null") {
		t.Fatalf("json with no seeds must have an empty list, got %s", js)
	}
}

func TestSeedPageRejectsBadInputAndEscapesHTML(t *testing.T) {
	s := newStore(t)
	srv := serve(t, s, challenge.Info{Mode: "pool"})

	for name, form := range map[string]url.Values{
		"empty seed":     {"action": {"set"}, "who": {"*"}, "seed": {""}},
		"too long":       {"action": {"set"}, "who": {"*"}, "seed": {strings.Repeat("x", 100)}},
		"bad scope":      {"action": {"set"}, "who": {"nobody"}, "seed": {"x"}},
		"unknown action": {"action": {"explode"}, "who": {"*"}},
	} {
		if code, body := post(t, srv, form); code != http.StatusBadRequest || !strings.Contains(body, "err") {
			t.Fatalf("%s: answered %d", name, code)
		}
	}

	// * a seed is shown back to other people on the network: it must never be interpreted as HTML
	post(t, srv, url.Values{"action": {"set"}, "who": {"*"}, "seed": {`<script>alert(1)</script>`}})

	if _, body := get(t, srv, "/seed"); strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("seed was not escaped:\n%s", body)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/seed", nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: %v %v", resp, err)
	}

	resp.Body.Close()
}

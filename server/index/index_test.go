package index

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testRecord(id int64) Record {
	sig := make([]byte, 256)
	for i := range sig {
		sig[i] = byte(id*31 + int64(i)*7)
	}

	attempts := 10 + id%500
	c := capture{Prefix: "https://cdn.example/ds/1/data/", ID: id, Rev: 1, Expires: 1624143146, Sig: sig, KeyPair: "KP1"}

	return Record{
		ID: id, Name: fmt.Sprintf("Level %d ミ", id), Creator: 1_000_000_000 + id%1000, Clears: (id * 37) % (attempts + 1), Attempts: attempts,
		Stars: 1 + id%30, Upload: time.Date(2015, 9, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute), URL: c.URL(),
	}
}

func buildIndex(t *testing.T, n int, shuffleSeed int64) (*Index, Stats, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "index.sqlite")

	b, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}

	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(1000 + i*3)
	}

	rand.New(rand.NewSource(shuffleSeed)).Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })

	for _, id := range ids {
		if err := b.Add(testRecord(id)); err != nil {
			t.Fatal(err)
		}
	}

	st, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}

	x, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { x.Close() })

	return x, st, path
}

func pct(l Level) int { return bucketOf(l.Clears, l.Attempts) }

func TestBuildCountsFingerprintAndLeftoverFiles(t *testing.T) {
	x, st, path := buildIndex(t, 20000, 1)

	if st.Total != 20000 || x.Total() != 20000 || st.Added != 20000 {
		t.Fatalf("totals: %+v / %d", st, x.Total())
	}

	var sum int64
	for _, c := range x.Counts() {
		sum += c
	}

	if sum != 20000 {
		t.Fatalf("bucket counts add up to %d", sum)
	}

	// * the same levels added in another order give the same fingerprint and the same file layout
	x2, st2, _ := buildIndex(t, 20000, 99)
	if st.Fingerprint != st2.Fingerprint || x.Fingerprint() != x2.Fingerprint() || len(st.Fingerprint) != 8 {
		t.Fatalf("fingerprints: %q %q", st.Fingerprint, st2.Fingerprint)
	}

	a, _ := x.Sample(NewSeeded("s", "c"), 0, 100, 50, nil)
	b, _ := x2.Sample(NewSeeded("s", "c"), 0, 100, 50, nil)

	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("insertion order changed what a seed selects")
		}
	}

	// * a different set of levels has a different fingerprint
	if y, _, _ := buildIndex(t, 19999, 1); y.Fingerprint() == x.Fingerprint() {
		t.Fatal("different level sets share a fingerprint")
	}

	if m, _ := filepath.Glob(path + "*"); len(m) != 1 {
		t.Fatalf("temporary files left behind: %v", m)
	}
}

func TestEveryLevelSurvivesTheRoundTrip(t *testing.T) {
	x, _, _ := buildIndex(t, 3000, 5)

	for i := 0; i < 3000; i += 7 {
		id := int64(1000 + i*3)
		want := testRecord(id)

		got, err := x.ByID(id)
		if err != nil || got == nil {
			t.Fatalf("level %d: %v %v", id, got, err)
		}

		if got.Name != want.Name || got.Creator != want.Creator || got.Clears != want.Clears || got.Attempts != want.Attempts ||
			got.Stars != want.Stars || !got.Upload.Equal(want.Upload) || got.URL != want.URL || got.Bucket != bucketOf(want.Clears, want.Attempts) {
			t.Fatalf("level %d changed:\n got  %+v\n want %+v", id, got, want)
		}

		up := got.Upstream()
		if up.LevelID != id || up.Failures != want.Attempts-want.Clears || up.URL != want.URL {
			t.Fatalf("upstream conversion wrong: %+v", up)
		}
	}

	if l, err := x.ByID(5); l != nil || err != nil {
		t.Fatalf("unknown id: %v %v", l, err)
	}
}

func TestBadRecordsAreSkippedWithReasons(t *testing.T) {
	b, err := Create(filepath.Join(t.TempDir(), "i.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()

	good := testRecord(1000)

	cases := map[string]Record{}
	r := good
	r.ID = 0
	cases["no id"] = r
	r = good
	r.Attempts, r.Clears = 0, 0
	cases["no attempts"] = r
	r = good
	r.URL = "https://example.com/nothing"
	cases["bad url"] = r
	r = good
	r.ID = 2000
	cases["url for another level"] = r

	for name, rec := range cases {
		var se *SkipError
		if err := b.Add(rec); err == nil {
			t.Fatalf("%s: accepted", name)
		} else if se2, ok := err.(*SkipError); !ok || se2.Reason == "" {
			t.Fatalf("%s: want a SkipError with a reason, got %v", name, err)
		} else {
			se = se2
			_ = se
		}
	}

	if err := b.Add(good); err != nil {
		t.Fatal(err)
	}

	if err := b.Add(good); err != nil { // the same level twice: the later record replaces the earlier
		t.Fatal(err)
	}

	st, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}

	if st.Total != 1 || st.SkippedNoAttempts != 1 || st.SkippedBad != 3 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestSampleRespectsTheRangeIsDistinctAndUniform(t *testing.T) {
	x, _, _ := buildIndex(t, 30000, 3)
	src := NewSeeded("uniform", "test")

	ls, err := x.Sample(src, 20, 50, 400, nil)
	if err != nil || len(ls) != 400 {
		t.Fatalf("got %d levels (%v)", len(ls), err)
	}

	seen := map[int64]bool{}

	for _, l := range ls {
		if p := pct(l); p < 20 || p > 50 || l.Bucket != p {
			t.Fatalf("level %d has clear rate %d%%, outside 20-50", l.ID, p)
		}

		if seen[l.ID] {
			t.Fatalf("level %d returned twice", l.ID)
		}

		seen[l.ID] = true
	}

	// * uniform over LEVELS: each bucket should show up in proportion to how many levels it holds
	counts := x.Counts()

	var total int64
	for b := 20; b <= 50; b++ {
		total += counts[b]
	}

	got := map[int]int{}

	for i := 0; i < 60; i++ {
		picked, _ := x.Sample(src, 20, 50, 100, nil)
		for _, l := range picked {
			got[l.Bucket]++
		}
	}

	for b := 20; b <= 50; b++ {
		want := 6000 * float64(counts[b]) / float64(total)
		if want > 30 && (float64(got[b]) < 0.6*want || float64(got[b]) > 1.4*want) {
			t.Fatalf("bucket %d drawn %d times, expected about %.0f", b, got[b], want)
		}
	}
}

func TestSeededSamplesAreRepeatableAndPrefixConsistent(t *testing.T) {
	x, _, _ := buildIndex(t, 30000, 4)

	ids := func(ls []Level) []int64 {
		out := make([]int64, len(ls))
		for i, l := range ls {
			out[i] = l.ID
		}

		return out
	}

	same := func(a, b []int64) bool {
		if len(a) != len(b) {
			return false
		}

		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}

		return true
	}

	draw := func(seed, ctx string, n int) []int64 {
		ls, err := x.Sample(NewSeeded(seed, ctx), 1, 49, n, nil)
		if err != nil {
			t.Fatal(err)
		}

		return ids(ls)
	}

	a := draw("Friday Race", "range|300000000|1|49", 100)

	if !same(a, draw("friday   race", "range|300000000|1|49", 100)) {
		t.Fatal("the same seed gave different levels")
	}

	if !same(a[:32], draw("friday race", "range|300000000|1|49", 32)) {
		t.Fatal("a shorter request must be the start of a longer one")
	}

	if same(a, draw("friday race 2", "range|300000000|1|49", 100)) || same(a, draw("friday race", "range|300000000|50|100", 100)) {
		t.Fatal("a different seed or list gave the same levels")
	}

	r1, _ := x.Sample(NewRandom(), 1, 49, 100, nil)
	r2, _ := x.Sample(NewRandom(), 1, 49, 100, nil)

	if same(ids(r1), ids(r2)) {
		t.Fatal("unseeded draws should differ")
	}

	// * levels marked dead are skipped, and skipping is itself repeatable
	dead := map[int64]bool{a[0]: true, a[3]: true, a[10]: true}
	skip := func(id int64) bool { return dead[id] }

	s1, _ := x.Sample(NewSeeded("friday race", "range|300000000|1|49"), 1, 49, 100, skip)
	s2, _ := x.Sample(NewSeeded("friday race", "range|300000000|1|49"), 1, 49, 100, skip)

	if !same(ids(s1), ids(s2)) || len(s1) != 100 {
		t.Fatal("skipping broke repeatability")
	}

	for _, l := range s1 {
		if dead[l.ID] {
			t.Fatalf("dead level %d returned", l.ID)
		}
	}
}

func TestSmallAndEmptyRanges(t *testing.T) {
	x, _, _ := buildIndex(t, 5000, 6)

	counts := x.Counts()
	b := 0

	for i, c := range counts {
		if c > 0 && c < 60 {
			b = i
			break
		}
	}

	if counts[b] == 0 {
		t.Skip("no small bucket in this data set")
	}

	// * asking for more levels than exist returns each exactly once
	ls, err := x.Sample(NewSeeded("a", "b"), b, b, 200, nil)
	if err != nil || int64(len(ls)) != counts[b] {
		t.Fatalf("bucket %d holds %d levels, got %d (%v)", b, counts[b], len(ls), err)
	}

	for _, bad := range [][3]int{{50, 40, 10}, {101, 200, 10}, {0, 100, 0}, {0, 100, -1}} {
		if out, err := x.Sample(NewSeeded("a", "b"), bad[0], bad[1], bad[2], nil); out != nil || err != nil {
			t.Fatalf("%v: %v %v", bad, out, err)
		}
	}

	// * out-of-range bounds are clamped, not an error
	if out, _ := x.Sample(NewSeeded("a", "b"), -50, 500, 5, nil); len(out) != 5 {
		t.Fatalf("clamping failed: %d levels", len(out))
	}
}

func TestNewestAndTopStarsAreOrderedAndPaged(t *testing.T) {
	x, _, _ := buildIndex(t, 3000, 7)

	newest, err := x.Newest(0, 50)
	if err != nil || len(newest) != 50 {
		t.Fatalf("%d %v", len(newest), err)
	}

	for i := 1; i < len(newest); i++ {
		if newest[i].Upload.After(newest[i-1].Upload) {
			t.Fatal("newest is not newest first")
		}
	}

	page2, _ := x.Newest(10, 5)
	for i := range page2 {
		if page2[i].ID != newest[10+i].ID {
			t.Fatal("offset paging is inconsistent")
		}
	}

	top, _ := x.TopStars(0, 100)
	for i := 1; i < len(top); i++ {
		if top[i].Stars > top[i-1].Stars {
			t.Fatal("top stars is not ordered")
		}
	}

	if out, _ := x.Newest(0, 0); out != nil {
		t.Fatal("n=0 should return nothing")
	}
}

func TestFinishReplacesAnExistingIndexAndAbortCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")

	for round, n := range []int{10, 25} {
		b, err := Create(path)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < n; i++ {
			if err := b.Add(testRecord(int64(1000 + i))); err != nil {
				t.Fatal(err)
			}
		}

		if st, err := b.Finish(); err != nil || st.Total != int64(n) {
			t.Fatalf("round %d: %+v %v", round, st, err)
		}
	}

	x, err := Open(path)
	if err != nil || x.Total() != 25 {
		t.Fatalf("expected the second build to win: %v", err)
	}

	x.Close()

	b, _ := Create(filepath.Join(dir, "other.sqlite"))
	b.Add(testRecord(1))
	b.Abort()

	if m, _ := filepath.Glob(filepath.Join(dir, "other.sqlite*")); len(m) != 0 {
		t.Fatalf("Abort left files behind: %v", m)
	}

	if _, err := Open(filepath.Join(dir, "missing.sqlite")); err == nil {
		t.Fatal("opening a missing index must fail")
	}

	if err := os.WriteFile(filepath.Join(dir, "junk.sqlite"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(filepath.Join(dir, "junk.sqlite")); err == nil {
		t.Fatal("opening a non-index must fail")
	}
}

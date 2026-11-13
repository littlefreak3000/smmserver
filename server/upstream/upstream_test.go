package upstream_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

const record = `{"url":"https://cdn.example/10.WUP_AMAJ_datastore/ds/1/data/00001829603-00001?Expires=1624143146&Signature=abc~def&Key-Pair-Id=K",
"name":"Mini castle destruction","creator":"Zaxonork","levelid":1829603,"creatorid":1799947827,"clears":352,"failures":221,
"total_attempts":573,"clearrate":0.6143106457242583,"uploadTime":"2015-09-12T01:07:16.000Z","world_record_ms":14142,
"world_record_holder_nnid":"Popcorn_Alt","stars":35,"downloads":0}`

func newClient(base, wayback string) *upstream.Client {
	c := upstream.New(base, wayback, time.Millisecond, time.Millisecond)
	c.HTTP = &http.Client{Timeout: 5 * time.Second}

	return c
}

func TestRandomLevelShapesAndMisses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/1"):
			fmt.Fprint(w, "["+record+"]")
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/2"):
			fmt.Fprint(w, `{"error":"No Random Level Found!"}`)
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/3"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/4"): // numbers as strings, no clearrate
			fmt.Fprint(w, `[{"url":"https://x/y","name":"s","levelid":"77","creatorid":"5","clears":"1","failures":"3","total_attempts":"4","clearrate":"0","uploadTime":"nonsense","stars":"0"}]`)
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/5"):
			fmt.Fprint(w, `[{"name":"no url or id"}]`)
		case strings.HasSuffix(r.URL.Path, "/searchRandomLevels/6"):
			fmt.Fprint(w, `<html>not json</html>`)
		}
	}))
	defer srv.Close()

	c := newClient(srv.URL, "")
	ctx := context.Background()

	l, err := c.RandomLevel(ctx, 1)
	if err != nil || l == nil {
		t.Fatalf("level: %v %v", l, err)
	}

	if l.LevelID != 1829603 || l.CreatorID != 1799947827 || l.Clears != 352 || l.TotalAttempts != 573 || l.Stars != 35 ||
		l.WorldRecordMs != 14142 || l.WorldRecordHolderNNID != "Popcorn_Alt" || l.Name != "Mini castle destruction" {
		t.Fatalf("parsed wrong: %+v", l)
	}

	if l.ClearRate < 0.6143 || l.ClearRate > 0.6144 || l.UploadTime.Year() != 2015 || l.UploadTime.Location() != time.UTC {
		t.Fatalf("rate/time wrong: %v %v", l.ClearRate, l.UploadTime)
	}

	if !strings.Contains(l.URL, "Expires=1624143146&Signature=abc~def") {
		t.Fatalf("url mangled: %s", l.URL)
	}

	for _, n := range []int{2, 3} {
		if l, err := c.RandomLevel(ctx, n); l != nil || err != nil {
			t.Fatalf("index %d should be an empty miss, got %v %v", n, l, err)
		}
	}

	l, err = c.RandomLevel(ctx, 4)
	if err != nil || l == nil || l.LevelID != 77 || l.Clears != 1 || l.TotalAttempts != 4 || l.UploadTime.IsZero() {
		t.Fatalf("string numbers: %+v %v", l, err)
	}

	if l.ClearRate != 0.25 {
		t.Fatalf("clear rate not derived from clears/attempts: %v", l.ClearRate)
	}

	if _, err := c.RandomLevel(ctx, 5); err == nil {
		t.Fatal("a record without url/levelid must be an error")
	}

	if _, err := c.RandomLevel(ctx, 6); err == nil {
		t.Fatal("non-JSON must be an error")
	}
}

func TestLevelByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/level/1829603"):
			fmt.Fprint(w, record) // object
		case strings.HasSuffix(r.URL.Path, "/level/5"):
			fmt.Fprint(w, "["+record+"]") // array
		case strings.HasSuffix(r.URL.Path, "/level/404"):
			http.NotFound(w, r)
		default:
			fmt.Fprint(w, `{"error":"Level not found"}`)
		}
	}))
	defer srv.Close()

	c := newClient(srv.URL, "")

	for _, id := range []int64{1829603, 5} {
		if l, err := c.Level(context.Background(), id); err != nil || l == nil || l.LevelID != 1829603 {
			t.Fatalf("level %d: %v %v", id, l, err)
		}
	}

	for _, id := range []int64{404, 9} {
		if l, err := c.Level(context.Background(), id); l != nil || err != nil {
			t.Fatalf("unknown level %d: %v %v", id, l, err)
		}
	}
}

func TestFetchBinUsesRawCaptureURLAndRejectsBadAnswers(t *testing.T) {
	payload := []byte("ASH0-pretend-level-bytes")

	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.URL.Path + "?" + r.URL.RawQuery)

		switch {
		case strings.Contains(r.URL.Path, "00000000001-00001"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "00000000002-00001"):
			// empty body
		case strings.Contains(r.URL.Path, "00000000003-00001"):
			w.Write(make([]byte, 5<<20)) // too big to be a level
		default:
			w.Write(payload)
		}
	}))
	defer srv.Close()

	c := newClient("", srv.URL)
	ctx := context.Background()

	orig := "https://cdn.example/10.WUP_AMAJ_datastore/ds/1/data/00001829603-00001?Expires=1624143146&Signature=abc~def&Key-Pair-Id=K"

	got, err := c.FetchBin(ctx, orig)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("fetch: %q %v", got, err)
	}

	// * Wayback "id_" flavour (raw bytes), and the original URL including its query is passed through verbatim
	if s := seen.Load().(string); !strings.HasPrefix(s, "/web/2id_/https://cdn.example/") || !strings.HasSuffix(s, "?Expires=1624143146&Signature=abc~def&Key-Pair-Id=K") {
		t.Fatalf("request was %q", s)
	}

	if _, err := c.FetchBin(ctx, "https://cdn.example/data/00000000001-00001?x=1"); !errors.Is(err, upstream.ErrNotFound) {
		t.Fatalf("404 should be ErrNotFound, got %v", err)
	}

	if _, err := c.FetchBin(ctx, "https://cdn.example/data/00000000002-00001?x=1"); !errors.Is(err, upstream.ErrNotFound) {
		t.Fatalf("empty body should be ErrNotFound, got %v", err)
	}

	if _, err := c.FetchBin(ctx, "https://cdn.example/data/00000000003-00001?x=1"); err == nil {
		t.Fatal("an oversized body must be rejected")
	}
}

func TestPacingRetryAfterAndCancellation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path == "/searchRandomLevels/429" && n <= 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		fmt.Fprint(w, `{"error":"No Random Level Found!"}`)
	}))
	defer srv.Close()

	// * pacing: 4 calls at a 60 ms minimum interval cannot finish in under ~180 ms
	c := upstream.New(srv.URL, "", 60*time.Millisecond, time.Millisecond)
	start := time.Now()

	for i := 0; i < 4; i++ {
		if _, err := c.RandomLevel(context.Background(), i); err != nil {
			t.Fatal(err)
		}
	}

	if d := time.Since(start); d < 170*time.Millisecond {
		t.Fatalf("4 calls took %v; pacing is not enforced", d)
	}

	// * 429 with Retry-After: 1 -> waits about a second, then succeeds
	calls.Store(0)
	start = time.Now()

	if _, err := c.RandomLevel(context.Background(), 429); err != nil {
		t.Fatalf("should recover after Retry-After: %v", err)
	}

	if d := time.Since(start); d < 900*time.Millisecond || calls.Load() != 2 {
		t.Fatalf("retry took %v with %d calls", d, calls.Load())
	}

	// * cancellation while waiting for a Retry-After returns promptly
	calls.Store(0)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer slow.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start = time.Now()

	if _, err := upstream.New(slow.URL, "", time.Millisecond, time.Millisecond).RandomLevel(ctx, 1); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel not honored: err=%v after %v", err, time.Since(start))
	}
}

func TestNeverCallsAnalyticsEndpoints(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, "["+record+"]")
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.URL)
	c.RandomLevel(context.Background(), 1)
	c.Level(context.Background(), 1)
	c.FetchBin(context.Background(), "https://cdn.example/x")

	for _, p := range paths {
		if strings.Contains(p, "firststart") || strings.Contains(p, "countdownload") {
			t.Fatalf("analytics endpoint called: %s", p)
		}
	}

	if len(paths) != 3 {
		t.Fatalf("paths: %v", paths)
	}
}

// The per-level answer (/level/<id>) carries what the random-level answer lacks: the PIDs of the record holder and of
// the first player to clear the level, and the record's dates. (Shape copied from a real answer; Mii data left out.)
func TestPerLevelAnswerCarriesTheRecord(t *testing.T) {
	l, err := upstream.ParseRecord([]byte(`{"EntryID":7556644,"clears":326,"creator_pid":1796647197,"failures":11,"total_attempts":337,
"clears_total_attempts":0.9673590504451038,"id":1288033,"stars":35,"upload_time":"2015-09-11T08:45:33.000Z",
"world_record_best_time_ms":11360,"world_record_best_time_player_nnid":"Ohm7101","world_record_best_time_player_pid":1761895654,
"world_record_created_time":"2015-12-24T16:09:33.000Z","world_record_first_complete_player_nnid":"krausm",
"world_record_first_complete_player_pid":1786067547,"world_record_updated_time":"2017-03-29T08:30:57.000Z","downloads":0,
"levelid":1288033,"name":"ALL","creator":"mine2980","creatorid":1796647197,"clearrate":0.9673590504451038,
"uploadTime":"2015-09-11T08:45:33.000Z","world_record_ms":11360,"world_record_holder_nnid":"Ohm7101",
"url":"https://cdn.example/10.WUP_AMAJ_datastore/ds/1/data/00001288033-00001?Expires=1&Signature=a&Key-Pair-Id=K"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !l.HasRecord() || l.WorldRecordMs != 11360 || l.WorldRecordPID != 1761895654 || l.WorldRecordHolderNNID != "Ohm7101" ||
		l.FirstClearPID != 1786067547 || l.FirstClearNNID != "krausm" ||
		l.WorldRecordCreated.Year() != 2015 || l.WorldRecordUpdated.Year() != 2017 {
		t.Fatalf("record not read: %+v", l)
	}

	// the random-level answer has the time and the holder's name but no PIDs: not storable
	r, err := upstream.ParseRecord([]byte(record))
	if err != nil {
		t.Fatal(err)
	}

	if r.WorldRecordMs != 14142 || r.HasRecord() {
		t.Fatalf("random-level answer: ms=%d HasRecord=%v", r.WorldRecordMs, r.HasRecord())
	}
}

// The Mii data of the people of a level is read as the 140-byte blob the game keeps in a maker's profile object.
func TestPerLevelAnswerCarriesTheMiis(t *testing.T) {
	blob := make([]byte, 140)
	copy(blob, "BPFC")

	good := base64.StdEncoding.EncodeToString(blob)

	l, err := upstream.ParseRecord([]byte(fmt.Sprintf(`{"url":"https://cdn.example/d/00000000042-00001","levelid":42,"creatorid":5,"creator":"Maker",
"creator_mii_data":%q,"world_record_best_time_player_mii_data":"not base64!","world_record_first_complete_player_mii_data":""}`, good)))
	if err != nil {
		t.Fatal(err)
	}

	if len(l.CreatorMii) != 140 || string(l.CreatorMii[:4]) != "BPFC" {
		t.Fatalf("creator Mii not read: %d bytes", len(l.CreatorMii))
	}

	if l.WorldRecordMii != nil || l.FirstClearMii != nil {
		t.Fatal("garbage or empty Mii data must be ignored")
	}
}

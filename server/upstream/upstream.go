// Package upstream talks to the two public services that hold the archived Super Mario Maker 1 levels:
//
//   - a metadata API (the one HerobrineTV's SMM1 Level Downloader uses): one random level per call, or a
//     level by ID, with clears / attempts / clear rate / stars / creator and the original download URL;
//   - the Internet Archive's Wayback Machine, which captured those original download URLs in 2021.
//
// Both are run by volunteers, so every request is paced (minimum interval per service, shared by all
// callers), retried only a few times, honors Retry-After, and carries an identifying User-Agent. The metadata
// API's analytics endpoints (/firststart, /countdownload) are deliberately never called.
package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAPI     = "https://api.bobac-analytics.com/smm1"
	DefaultWayback = "https://web.archive.org"
	maxBinSize     = 4 << 20 // levels are ~25-100 KB; anything bigger is not a level
)

// ErrNotFound means the service answered but has no such level / capture.
var ErrNotFound = errors.New("upstream: not found")

// flexInt / flexFloat accept both JSON numbers and numeric strings (the API is not consistent).
type flexInt int64
type flexFloat float64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}

	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
		return nil
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}

	*f = flexInt(v)

	return nil
}

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}

	*f = flexFloat(v)

	return nil
}

// Level is one record of the metadata API.
type Level struct {
	URL                   string
	Name                  string
	Creator               string
	LevelID               int64
	CreatorID             int64
	Clears                int64
	Failures              int64
	TotalAttempts         int64
	ClearRate             float64 // clears / attempts, 0..1
	UploadTime            time.Time
	WorldRecordMs         int64
	WorldRecordHolderNNID string
	Stars                 int64

	// * only the per-level answer (Client.Level) carries these; the random-level answer has no PIDs
	WorldRecordPID     int64 // who holds the world record
	FirstClearPID      int64 // who cleared it first
	FirstClearNNID     string
	WorldRecordCreated time.Time // when the first record was set
	WorldRecordUpdated time.Time // when it was last beaten

	// * Mii data (the same 140-byte "BPFC" blob the game keeps in a maker's profile object) of the people of a level
	CreatorMii     []byte
	WorldRecordMii []byte
	FirstClearMii  []byte
}

// HasRecord reports whether the level carries a world record that can be stored (a time and who set it).
func (l Level) HasRecord() bool { return l.WorldRecordMs > 0 && l.WorldRecordPID != 0 }

type levelJSON struct {
	URL       string    `json:"url"`
	Name      string    `json:"name"`
	Creator   string    `json:"creator"`
	LevelID   flexInt   `json:"levelid"`
	CreatorID flexInt   `json:"creatorid"`
	Clears    flexInt   `json:"clears"`
	Failures  flexInt   `json:"failures"`
	Attempts  flexInt   `json:"total_attempts"`
	ClearRate flexFloat `json:"clearrate"`
	Upload    string    `json:"uploadTime"`
	WRms      flexInt   `json:"world_record_ms"`
	WRNNID    string    `json:"world_record_holder_nnid"`

	WRBestMs    flexInt `json:"world_record_best_time_ms"`
	WRBestPID   flexInt `json:"world_record_best_time_player_pid"`
	WRBestNNID  string  `json:"world_record_best_time_player_nnid"`
	WRFirstPID  flexInt `json:"world_record_first_complete_player_pid"`
	WRFirstNNID string  `json:"world_record_first_complete_player_nnid"`
	CreatorMii  string  `json:"creator_mii_data"`
	WRBestMii   string  `json:"world_record_best_time_player_mii_data"`
	WRFirstMii  string  `json:"world_record_first_complete_player_mii_data"`
	WRCreatedAt string  `json:"world_record_created_time"`
	WRUpdatedAt string  `json:"world_record_updated_time"`
	Stars       flexInt `json:"stars"`
	Error       string  `json:"error"`
}

func (j levelJSON) level() (*Level, error) {
	if j.URL == "" || j.LevelID <= 0 {
		return nil, errors.New("upstream: record has no url / levelid")
	}

	l := &Level{
		URL: j.URL, Name: j.Name, Creator: j.Creator, LevelID: int64(j.LevelID), CreatorID: int64(j.CreatorID),
		Clears: int64(j.Clears), Failures: int64(j.Failures), TotalAttempts: int64(j.Attempts),
		ClearRate: float64(j.ClearRate), WorldRecordMs: int64(j.WRms), WorldRecordHolderNNID: j.WRNNID, Stars: int64(j.Stars),
	}

	l.CreatorMii, l.WorldRecordMii, l.FirstClearMii = decodeMii(j.CreatorMii), decodeMii(j.WRBestMii), decodeMii(j.WRFirstMii)

	if l.WorldRecordMs == 0 {
		l.WorldRecordMs = int64(j.WRBestMs)
	}

	if l.WorldRecordHolderNNID == "" {
		l.WorldRecordHolderNNID = j.WRBestNNID
	}

	l.WorldRecordPID, l.FirstClearPID, l.FirstClearNNID = int64(j.WRBestPID), int64(j.WRFirstPID), j.WRFirstNNID

	if t, err := time.Parse(time.RFC3339Nano, j.WRCreatedAt); err == nil {
		l.WorldRecordCreated = t.UTC()
	}

	if t, err := time.Parse(time.RFC3339Nano, j.WRUpdatedAt); err == nil {
		l.WorldRecordUpdated = t.UTC()
	}

	// * clear rate is clears / attempts; derive it when the API omits it
	if l.TotalAttempts > 0 && l.ClearRate == 0 && l.Clears > 0 {
		l.ClearRate = float64(l.Clears) / float64(l.TotalAttempts)
	}

	if t, err := time.Parse(time.RFC3339Nano, j.Upload); err == nil {
		l.UploadTime = t.UTC()
	} else {
		l.UploadTime = time.Date(2015, 9, 10, 0, 0, 0, 0, time.UTC) // game release; better than the zero time
	}

	return l, nil
}

// decodeMii reads the base64 Mii data of the API; nil for anything that is not a plausible Mii blob.
func decodeMii(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) < 16 || string(b[:4]) != "BPFC" {
		return nil
	}

	return b
}

// ParseRecord decodes one metadata record: a JSON object shaped like the API's answer, for example one line of a JSONL dump.
func ParseRecord(line []byte) (*Level, error) {
	var j levelJSON
	if err := json.Unmarshal(line, &j); err != nil {
		return nil, err
	}

	if j.Error != "" {
		return nil, errors.New(j.Error)
	}

	return j.level()
}

type limiter struct {
	mu    sync.Mutex
	next  time.Time
	every time.Duration
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}

	at := l.next
	l.next = at.Add(l.every)
	l.mu.Unlock()

	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()

		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// Client is safe for concurrent use; the pacing is shared by all callers.
type Client struct {
	APIBase     string
	WaybackBase string
	UserAgent   string
	HTTP        *http.Client
	Logf        func(format string, args ...any)

	apiLimit, wbLimit limiter
}

// New returns a client with polite defaults: at most one API call per apiEvery and one Wayback request per
// wbEvery (zero uses 400 ms / 1 s).
func New(apiBase, waybackBase string, apiEvery, wbEvery time.Duration) *Client {
	if apiBase == "" {
		apiBase = DefaultAPI
	}

	if waybackBase == "" {
		waybackBase = DefaultWayback
	}

	if apiEvery == 0 {
		apiEvery = 400 * time.Millisecond
	}

	if wbEvery == 0 {
		wbEvery = time.Second
	}

	return &Client{
		APIBase:     strings.TrimRight(apiBase, "/"),
		WaybackBase: strings.TrimRight(waybackBase, "/"),
		UserAgent:   "smm-home-server (self-hosted Super Mario Maker archive; low-rate)",
		HTTP:        &http.Client{Timeout: 40 * time.Second},
		apiLimit:    limiter{every: apiEvery},
		wbLimit:     limiter{every: wbEvery},
	}
}

func (c *Client) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// get performs a paced GET with up to 3 attempts. It retries network errors, 429 and 5xx, honoring
// Retry-After (capped at 30 s). 404 returns ErrNotFound; other statuses are errors.
func (c *Client) get(ctx context.Context, lim *limiter, rawURL string, limit int64) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if err := lim.wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("User-Agent", c.UserAgent)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			lastErr = err
			continue
		}

		body, rerr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			if int64(len(body)) > limit {
				return nil, fmt.Errorf("upstream: response larger than %d bytes", limit)
			}

			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("upstream: HTTP %d from %s", resp.StatusCode, hostOf(rawURL))

			wait := time.Duration(attempt+1) * 2 * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(ra) * time.Second
			}

			if wait > 30*time.Second {
				wait = 30 * time.Second
			}

			c.logf("upstream: %v, retrying in %s", lastErr, wait)

			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			if rerr != nil {
				return nil, rerr
			}

			return nil, fmt.Errorf("upstream: HTTP %d from %s", resp.StatusCode, hostOf(rawURL))
		}
	}

	return nil, fmt.Errorf("upstream: giving up on %s: %w", hostOf(rawURL), lastErr)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}

	return raw
}

// parseOne handles the API's shapes: an array of records, a single record, or {"error": "..."}.
func parseOne(body []byte) (*Level, error) {
	var arr []levelJSON
	if err := json.Unmarshal(body, &arr); err == nil {
		if len(arr) == 0 {
			return nil, nil
		}

		if arr[0].Error != "" {
			return nil, nil
		}

		return arr[0].level()
	}

	var one levelJSON
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, fmt.Errorf("upstream: unreadable response: %w", err)
	}

	if one.Error != "" {
		// * "No Random Level Found!" and similar mean "nothing at this index", not a failure
		return nil, nil
	}

	return one.level()
}

// RandomLevel asks the API for the level at pseudo-index n (0 <= n < RandomSpace). It returns (nil, nil)
// when nothing exists at that index, which is common: roughly half of all indexes are empty.
func (c *Client) RandomLevel(ctx context.Context, n int) (*Level, error) {
	body, err := c.get(ctx, &c.apiLimit, fmt.Sprintf("%s/searchRandomLevels/%d", c.APIBase, n), 1<<20)
	if err != nil {
		return nil, err
	}

	return parseOne(body)
}

// RandomSpace is the upper bound of the random index the public downloader uses.
const RandomSpace = 18118278

// Level fetches one level's metadata by ID; (nil, nil) if the API does not know it.
func (c *Client) Level(ctx context.Context, id int64) (*Level, error) {
	body, err := c.get(ctx, &c.apiLimit, fmt.Sprintf("%s/level/%d", c.APIBase, id), 1<<20)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return parseOne(body)
}

// FetchBin downloads the archived level file for an original download URL from the Wayback Machine. The
// "id_" form returns the captured bytes untouched; the timestamp "2" makes Wayback redirect to the closest
// (here: only) capture. The caller validates that the bytes are a course file.
func (c *Client) FetchBin(ctx context.Context, originalURL string) ([]byte, error) {
	// * the original URL, including its (expired) query string, is appended verbatim
	body, err := c.get(ctx, &c.wbLimit, c.WaybackBase+"/web/2id_/"+originalURL, maxBinSize)
	if err != nil {
		return nil, err
	}

	if len(body) == 0 {
		return nil, ErrNotFound
	}

	return body, nil
}

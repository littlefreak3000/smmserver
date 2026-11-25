// Package webui is the server's web page: a level viewer for what the catalog holds and an editor for settings.env. It
// listens on its own port, on this PC only unless told otherwise, and has no passwords: what it can change is the settings
// file, so it must not be reachable from the network by default.
package webui

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PretendoNetwork/super-mario-maker/ash0"
	"github.com/PretendoNetwork/super-mario-maker/coursecode"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
)

//go:embed static
var static embed.FS

// Config is what the page needs from the server.
type Config struct {
	DB              *sql.DB
	CoursesDir      string
	UploadsDir      string // where levels players upload are kept (tried after CoursesDir)
	SettingsPath    string
	Template        string                        // the commented settings template
	ValidateSetting func(key, value string) error // optional: judgement only the server can make
	Info            func() map[string]any         // optional: facts for the status line (addresses, ports)
}

type server struct {
	cfg      Config
	settings []Setting
	file     File
	running  map[string]string // what each setting was when the server started
}

// New returns the handler of the whole page.
func New(cfg Config) http.Handler {
	s := &server{cfg: cfg, settings: ParseTemplate(cfg.Template), file: File{Path: cfg.SettingsPath, Template: cfg.Template}, running: map[string]string{}}

	for _, st := range s.settings {
		s.running[st.Key] = os.Getenv(st.Key)
	}

	sub, _ := fs.Sub(static, "static")
	files := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("POST /api/settings", s.postSettings)
	mux.HandleFunc("GET /api/levels", s.levels)
	mux.HandleFunc("GET /api/level/{id}", s.level)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /thumb/{id}", s.thumb)
	mux.Handle("GET /", files)

	return guard(mux)
}

// Listen opens the page's port. host is "" for this PC only.
func Listen(host, port string) (net.Listener, error) {
	if host == "" {
		host = "127.0.0.1"
	}

	return net.Listen("tcp", net.JoinHostPort(host, port))
}

// guard refuses requests that a web page on another site could send to this PC ("DNS rebinding" and cross-site posts): the
// address in the request must be an IP address or localhost, and a change must come as JSON.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}

		host = strings.Trim(host, "[]")
		if host != "localhost" && net.ParseIP(host) == nil {
			http.Error(w, "use the PC's IP address or localhost", http.StatusForbidden)
			return
		}

		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "JSON only", http.StatusUnsupportedMediaType)
			return
		}

		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				http.Error(w, "cross-site request", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- settings ----

type settingView struct {
	Setting
	Value   string `json:"value"`   // set in the file ("" = the default is used)
	Pending bool   `json:"pending"` // changed since the server started: needs a restart
}

func (s *server) view() ([]settingView, error) {
	vals, err := s.file.Values()
	if err != nil {
		return nil, err
	}

	out := make([]settingView, 0, len(s.settings))

	for _, st := range s.settings {
		v := vals[st.Key]
		// * what the server runs with now is its environment as it started; an unset value means the default
		now, next := s.running[st.Key], v

		if now == "" {
			now = st.Default
		}

		if next == "" {
			next = st.Default
		}

		out = append(out, settingView{Setting: st, Value: v, Pending: now != next})
	}

	return out, nil
}

func (s *server) getSettings(w http.ResponseWriter, _ *http.Request) {
	v, err := s.view()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": s.cfg.SettingsPath, "settings": v})
}

func (s *server) postSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Changes map[string]string `json:"changes"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "not JSON")
		return
	}

	if err := s.file.Apply(body.Changes, s.settings, s.cfg.ValidateSetting); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	s.getSettings(w, r)
}

// ---- status ----

func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{}

	if s.cfg.Info != nil {
		for k, v := range s.cfg.Info() {
			out[k] = v
		}
	}

	var n int
	if s.cfg.DB.QueryRow(levelWhere("SELECT COUNT(*) FROM datastore.objects o", "")).Scan(&n) == nil {
		out["levels"] = n
	}

	writeJSON(w, http.StatusOK, out)
}

// ---- levels ----

const courseFilter = ` WHERE o.data_type > 2 AND o.data_type < 50 AND o.upload_completed = 1 AND o.deleted = 0 AND o.size > 0`

func levelWhere(sel, extra string) string { return sel + courseFilter + extra }

type levelRow struct {
	ID        int64    `json:"id"`
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"`
	Created   string   `json:"created"`
	ClearRate *float64 `json:"clearRate"`
	Attempts  *int64   `json:"attempts"`
	Stars     *int64   `json:"stars"`
}

var sorts = map[string]string{
	"new":      "o.creation_date DESC, o.data_id DESC",
	"old":      "o.creation_date ASC, o.data_id ASC",
	"hardest":  "ls.clear_rate ASC, o.data_id",
	"easiest":  "ls.clear_rate DESC, o.data_id",
	"stars":    "ls.stars DESC, o.data_id",
	"attempts": "ls.attempts DESC, o.data_id",
	"id":       "o.data_id ASC",
}

func (s *server) levels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where, args := "", []any{}

	if text := strings.TrimSpace(q.Get("q")); text != "" {
		if id, ok := parseID(text); ok {
			where += " AND o.data_id = ?"

			args = append(args, id)
		} else {
			like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text) + "%"
			where += ` AND (o.name LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM datastore.objects p WHERE p.owner = o.owner AND p.data_type = 1 AND p.name LIKE ? ESCAPE '\'))`

			args = append(args, like, like)
		}
	}

	if v, err := strconv.ParseFloat(q.Get("min"), 64); err == nil {
		where += " AND ls.clear_rate >= ?"

		args = append(args, v/100)
	}

	if v, err := strconv.ParseFloat(q.Get("max"), 64); err == nil {
		where += " AND ls.clear_rate <= ?"

		args = append(args, v/100)
	}

	order, ok := sorts[q.Get("sort")]
	if !ok {
		order = sorts["new"]
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 48
	}

	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	from := ` FROM datastore.objects o LEFT JOIN datastore.level_sources ls ON ls.data_id = o.data_id`

	var total int
	if err := s.cfg.DB.QueryRow(levelWhere("SELECT COUNT(*)"+from, where), args...).Scan(&total); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows, err := s.cfg.DB.Query(levelWhere(`SELECT o.data_id, COALESCE(o.name, ''), COALESCE(o.creation_date, ''),
		COALESCE((SELECT p.name FROM datastore.objects p WHERE p.owner = o.owner AND p.data_type = 1 LIMIT 1), ''),
		ls.clear_rate, ls.attempts, ls.stars`+from, where)+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	out := []levelRow{}

	for rows.Next() {
		var l levelRow
		if err := rows.Scan(&l.ID, &l.Name, &l.Created, &l.Creator, &l.ClearRate, &l.Attempts, &l.Stars); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}

		l.Code = coursecode.Format(uint64(l.ID))
		out = append(out, l)
	}

	writeJSON(w, http.StatusOK, map[string]any{"total": total, "levels": out})
}

// parseID accepts a plain number or a course ID in the game's own format.
func parseID(text string) (int64, bool) {
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}

	if id, err := coursecode.Parse(text); err == nil {
		return int64(id), true
	}

	return 0, false
}

// chunks reads and splits a course file.
func (s *server) chunks(id int64) (*levelimport.Chunks, error) {
	name := strconv.FormatInt(id, 10) + ".bin"

	b, err := os.ReadFile(filepath.Join(s.cfg.CoursesDir, name))
	if errors.Is(err, os.ErrNotExist) && s.cfg.UploadsDir != "" {
		b, err = os.ReadFile(filepath.Join(s.cfg.UploadsDir, name))
	}

	if err != nil {
		return nil, err
	}

	return levelimport.Split(b)
}

func (s *server) level(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}

	var row levelRow

	err = s.cfg.DB.QueryRow(levelWhere(`SELECT o.data_id, COALESCE(o.name, ''), COALESCE(o.creation_date, ''),
		COALESCE((SELECT p.name FROM datastore.objects p WHERE p.owner = o.owner AND p.data_type = 1 LIMIT 1), ''),
		ls.clear_rate, ls.attempts, ls.stars FROM datastore.objects o LEFT JOIN datastore.level_sources ls ON ls.data_id = o.data_id`,
		" AND o.data_id = ?"), id).Scan(&row.ID, &row.Name, &row.Created, &row.Creator, &row.ClearRate, &row.Attempts, &row.Stars)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "the server does not hold that level (yet)")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	row.Code = coursecode.Format(uint64(row.ID))

	c, err := s.chunks(id)
	if err != nil {
		fail(w, http.StatusNotFound, "the level's file is missing or damaged: "+err.Error())
		return
	}

	areas := map[string]any{}

	for name, blob := range map[string][]byte{"main": c.Course, "sub": c.Subworld} {
		raw, err := ash0.Decompress(blob)
		if err != nil {
			continue
		}

		if course, err := ParseCourse(raw); err == nil {
			areas[name] = course
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"level": row, "areas": areas})
}

func (s *server) thumb(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("id"), ".jpg"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	c, err := s.chunks(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	blob := c.Thumbnail0
	if r.URL.Query().Get("n") == "1" {
		blob = c.Thumbnail1
	}

	raw, err := ash0.Decompress(blob)
	if err != nil || len(raw) < 10 {
		http.NotFound(w, r)
		return
	}

	// * the thumbnail is a JPEG that starts after an 8 byte header
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(raw[8:]) //nolint:errcheck
}

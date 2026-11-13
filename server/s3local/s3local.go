// Package s3local is a tiny S3 stand-in used by the home/archive build of the server.
//
// The Wii U / 3DS cannot talk to modern S3 (old TLS only), and the SMM server needs only a few
// operations: presigned GET (console downloads <id>.bin / <id>.jpg), presigned POST (console uploads),
// HEAD (server stats sizes) and GET ?location (minio-go). Signatures are NOT verified.
//
// Storage is flat by file name. PreservedDir is a read-only archive; UploadDir is writable and checked
// first. Only the last path segment of a key is used, so "bucket/prefix/123.bin", "prefix/123.bin" and
// "123.bin" are the same object (the server builds keys inconsistently).
package s3local

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	PreservedDir string
	UploadDir    string
	MaxUpload    int64 // bytes; 0 = 16 MiB
	Logf         func(format string, args ...any)
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// KeyToName reduces any key to a safe flat file name, or "".
func KeyToName(key string) string {
	parts := strings.Split(key, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		if parts[i] == "." || parts[i] == ".." || !nameRe.MatchString(parts[i]) {
			return ""
		}
		return parts[i]
	}
	return ""
}

type targetKind int

const (
	kindService targetKind = iota
	kindBucket
	kindObject
	kindBad
)

// parseTarget works for path-style (/bucket/key) and virtual-host style (/key).
func parseTarget(p string) (targetKind, string) {
	dec, err := url.PathUnescape(p)
	if err != nil {
		return kindBad, ""
	}
	var segs []string
	for _, s := range strings.Split(dec, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	switch {
	case len(segs) == 0:
		return kindService, ""
	case len(segs) == 1 && !strings.Contains(segs[0], "."):
		return kindBucket, segs[0] // object names always have an extension
	default:
		return kindObject, strings.Join(segs, "/")
	}
}

type server struct {
	cfg  Config
	etag sync.Map // path|size|mtime -> etag
}

// NewHandler returns the S3 http.Handler.
func NewHandler(cfg Config) http.Handler {
	if cfg.MaxUpload == 0 {
		cfg.MaxUpload = 16 << 20
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &server{cfg: cfg}
}

// Listen starts the handler on addr (e.g. ":9000") and returns the listener.
func Listen(addr string, cfg Config) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: NewHandler(cfg)}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	errBody []byte // the start of an error answer, for the log
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status >= 400 && len(w.errBody) < 400 {
		w.errBody = append(w.errBody, p...)
	}

	return w.ResponseWriter.Write(p)
}

func (s *server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := &statusWriter{ResponseWriter: rw, status: 200}
	defer func() {
		s.cfg.Logf("s3 %s %s %s -> %d", r.RemoteAddr, r.Method, r.URL.Path, w.status)

		if w.status >= 400 {
			// * a refused request is the one thing worth explaining: say what the client sent and what we answered
			s.cfg.Logf("s3 refused: content-type=%q length=%d answer=%s", r.Header.Get("Content-Type"), r.ContentLength, strings.TrimSpace(string(w.errBody)))
		}
	}()
	w.Header().Set("Server", "AmazonS3")

	kind, v := parseTarget(r.URL.Path)
	switch kind {
	case kindBad:
		xmlError(w, r, 400, "InvalidURI", "Could not parse the specified URI.", r.URL.Path)
	case kindService:
		body := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Buckets></Buckets></ListAllMyBucketsResult>`
		writeXML(w, r, 200, body)
	case kindBucket:
		s.bucket(w, r, v)
	default:
		s.object(w, r, v)
	}
}

func writeXML(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		io.WriteString(w, body) //nolint:errcheck
	}
}

func xmlError(w http.ResponseWriter, r *http.Request, status int, code, msg, resource string) {
	var e strings.Builder
	xml.EscapeText(&e, []byte(msg))
	var res strings.Builder
	xml.EscapeText(&res, []byte(resource))
	writeXML(w, r, status, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<Error><Code>%s</Code><Message>%s</Message><Resource>%s</Resource><RequestId>0</RequestId></Error>`, code, e.String(), res.String()))
}

func (s *server) find(name string) (string, os.FileInfo) {
	for _, dir := range []string{s.cfg.UploadDir, s.cfg.PreservedDir} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, st
		}
	}
	return "", nil
}

func (s *server) etagFor(p string, st os.FileInfo) string {
	k := fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().UnixNano())
	if v, ok := s.etag.Load(k); ok {
		return v.(string)
	}
	f, err := os.Open(p)
	if err != nil {
		return `""`
	}
	defer f.Close()
	h := md5.New()
	io.Copy(h, f) //nolint:errcheck
	e := `"` + hex.EncodeToString(h.Sum(nil)) + `"`
	s.etag.Store(k, e)
	return e
}

func (s *server) object(w *statusWriter, r *http.Request, key string) {
	name := KeyToName(key)
	if name == "" {
		xmlError(w, r, 400, "InvalidArgument", "Invalid object key", "/"+key)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		p, st := s.find(name)
		if p == "" {
			xmlError(w, r, 404, "NoSuchKey", "The specified key does not exist.", "/"+key)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			xmlError(w, r, 500, "InternalError", err.Error(), "/"+key)
			return
		}
		defer f.Close()
		ct := "application/octet-stream"
		if ext := strings.ToLower(filepath.Ext(name)); ext == ".jpg" || ext == ".jpeg" {
			ct = "image/jpeg"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("ETag", s.etagFor(p, st))
		http.ServeContent(w, r, name, st.ModTime(), f) // Range, Last-Modified, Content-Length, HEAD

	case http.MethodPut:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload))
		if err != nil {
			xmlError(w, r, 413, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed size", "/"+key)
			return
		}
		etag, err := s.write(name, data)
		if err != nil {
			xmlError(w, r, 500, "InternalError", err.Error(), "/"+key)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(200)

	case http.MethodDelete:
		// Only uploads can be deleted; the preserved archive is read-only. S3 answers 204 either way.
		if s.cfg.UploadDir != "" {
			os.Remove(filepath.Join(s.cfg.UploadDir, name)) //nolint:errcheck
		}
		w.WriteHeader(204)

	case http.MethodPost:
		s.post(w, r, key)

	default:
		xmlError(w, r, 405, "MethodNotAllowed", "The specified method is not allowed against this resource.", "/"+key)
	}
}

func (s *server) write(name string, data []byte) (string, error) {
	if s.cfg.UploadDir == "" {
		return "", fmt.Errorf("uploads are disabled")
	}
	if err := os.MkdirAll(s.cfg.UploadDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.cfg.UploadDir, "."+name+".*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.cfg.UploadDir, name)); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`, nil
}

// post handles a browser-style presigned POST: multipart/form-data with "key", policy fields and "file".
func (s *server) post(w *statusWriter, r *http.Request, urlKey string) {
	mr, err := r.MultipartReader()
	if err != nil {
		xmlError(w, r, 400, "InvalidArgument", "POST requires multipart/form-data", "/"+urlKey)
		return
	}
	fields := map[string]string{}
	var file []byte
	var fileName string
	var haveFile bool
	limit := s.cfg.MaxUpload
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			xmlError(w, r, 400, "MalformedPOSTRequest", "Could not parse multipart body", "/"+urlKey)
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(data)) > limit {
			xmlError(w, r, 413, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed size", "/"+urlKey)
			return
		}
		// * real S3 finds the file by its field name; the Wii U client sends it without a filename
		if part.FormName() == "file" {
			file, fileName, haveFile = data, part.FileName(), true
		} else {
			fields[strings.ToLower(part.FormName())] = string(data)
		}
	}
	if !haveFile {
		xmlError(w, r, 400, "InvalidArgument", "POST requires exactly one file upload per request.", "/"+urlKey)
		return
	}
	// The key field is authoritative (it is what the server signed); fall back to the URL.
	key := urlKey
	if k, ok := fields["key"]; ok {
		key = strings.ReplaceAll(k, "${filename}", fileName)
	}
	name := KeyToName(key)
	if name == "" {
		xmlError(w, r, 400, "InvalidArgument", "Invalid object key", "/"+urlKey)
		return
	}
	etag, err := s.write(name, file)
	if err != nil {
		xmlError(w, r, 500, "InternalError", err.Error(), "/"+urlKey)
		return
	}
	w.Header().Set("ETag", etag)
	loc := "http://" + r.Host + "/" + name
	w.Header().Set("Location", loc)
	if fields["success_action_status"] == "201" {
		var k, l, e strings.Builder
		xml.EscapeText(&k, []byte(name))
		xml.EscapeText(&l, []byte(loc))
		xml.EscapeText(&e, []byte(etag))
		writeXML(w, r, 201, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<PostResponse><Location>%s</Location><Key>%s</Key><ETag>%s</ETag></PostResponse>`, l.String(), k.String(), e.String()))
		return
	}
	w.Header().Set("Content-Length", "0")
	if fields["success_action_status"] == "200" {
		w.WriteHeader(200)
	} else {
		w.WriteHeader(204)
	}
}

func (s *server) bucket(w *statusWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodPost:
		// Presigned POST URLs point at the bucket itself (https://host/bucket); the key is in the form.
		s.post(w, r, bucket+"/__form__")
		return
	case http.MethodHead:
		w.WriteHeader(200)
		return
	case http.MethodGet:
	default:
		xmlError(w, r, 405, "MethodNotAllowed", "Method not allowed", "/"+bucket)
		return
	}

	q := r.URL.Query()
	if _, ok := q["location"]; ok {
		writeXML(w, r, 200, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`)
		return
	}

	// Minimal ListObjects / ListObjectsV2: flat, prefix matched against the file name, max 1000.
	prefix := q.Get("prefix")
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		prefix = prefix[i+1:]
	}
	seen := map[string]string{}
	for _, dir := range []string{s.cfg.PreservedDir, s.cfg.UploadDir} {
		if dir == "" {
			continue
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() && !strings.HasPrefix(n, ".") && strings.HasPrefix(n, prefix) {
				seen[n] = filepath.Join(dir, n)
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	max := 1000
	if m, err := strconv.Atoi(q.Get("max-keys")); err == nil && m > 0 && m < max {
		max = m
	}
	if len(names) > max {
		names = names[:max]
	}
	var items strings.Builder
	for _, n := range names {
		st, err := os.Stat(seen[n])
		if err != nil {
			continue
		}
		var esc strings.Builder
		xml.EscapeText(&esc, []byte(n))
		fmt.Fprintf(&items, `<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>%s</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
			esc.String(), st.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"), strings.ReplaceAll(s.etagFor(seen[n], st), `"`, "&quot;"), st.Size())
	}
	var bn, pf strings.Builder
	xml.EscapeText(&bn, []byte(bucket))
	xml.EscapeText(&pf, []byte(q.Get("prefix")))
	writeXML(w, r, 200, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys><IsTruncated>false</IsTruncated>%s</ListBucketResult>`,
		bn.String(), pf.String(), len(names), max, items.String()))
}

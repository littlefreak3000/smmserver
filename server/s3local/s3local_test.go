package s3local_test

// Tests use the real minio-go client, the same library (and the same calls) the SMM server uses:
// presigned GET, StatObject, presigned POST policy.

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/s3local"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type env struct {
	srv       *httptest.Server
	mc        *minio.Client
	preserved string
	uploads   string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{preserved: filepath.Join(dir, "courses"), uploads: filepath.Join(dir, "uploads")}
	os.MkdirAll(e.preserved, 0o755)

	e.srv = httptest.NewServer(s3local.NewHandler(s3local.Config{PreservedDir: e.preserved, UploadDir: e.uploads}))
	t.Cleanup(e.srv.Close)

	u, _ := url.Parse(e.srv.URL)
	mc, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4("local", "local", ""), Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	e.mc = mc
	return e
}

func (e *env) preserve(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.preserved, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, u string) (int, []byte, http.Header) {
	t.Helper()
	r, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b, r.Header
}

func TestPresignedGetAndStatWithMinioClient(t *testing.T) {
	e := setup(t)
	course := bytes.Repeat([]byte{0xAB, 0xCD, 0x01}, 30000)
	e.preserve(t, "942697.bin", course)

	// * same call the server makes in prepare_get_object / get_object_infos; also triggers ?location
	u, err := e.mc.PresignedGetObject(context.Background(), "smm", "course/942697.bin", 15*time.Minute, url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	code, body, h := get(t, u.String())
	if code != 200 || !bytes.Equal(body, course) || h.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("GET %d len=%d ct=%s", code, len(body), h.Get("Content-Type"))
	}

	// * StatObject is what the server uses to compare sizes
	for _, key := range []string{"course/942697.bin", "942697.bin", "a/b/c/942697.bin"} {
		info, err := e.mc.StatObject(context.Background(), "smm", key, minio.StatObjectOptions{})
		if err != nil || info.Size != int64(len(course)) {
			t.Fatalf("stat %s: %v size=%d", key, err, info.Size)
		}
	}

	if _, err := e.mc.StatObject(context.Background(), "smm", "course/404.bin", minio.StatObjectOptions{}); err == nil || minio.ToErrorResponse(err).Code != "NoSuchKey" {
		t.Fatalf("missing key: %v", err)
	}
}

func TestPresignedPostPolicyUploadWithMinioClient(t *testing.T) {
	e := setup(t)

	policy := minio.NewPostPolicy()
	policy.SetBucket("smm")
	policy.SetKey("course/123456.jpg")
	policy.SetExpires(time.Now().UTC().Add(15 * time.Minute))
	postURL, form, err := e.mc.PresignedPostPolicy(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}

	jpg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3, 0xFF, 0xD9}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range form {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "blob")
	fw.Write(jpg)
	mw.Close()

	resp, err := http.Post(postURL.String(), mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("POST status %d", resp.StatusCode)
	}

	// * server-side verification, as in complete_attach_file.go
	info, err := e.mc.StatObject(context.Background(), "smm", "course/123456.jpg", minio.StatObjectOptions{})
	if err != nil || info.Size != int64(len(jpg)) {
		t.Fatalf("stat after upload: %v size=%d", err, info.Size)
	}
	if _, err := os.Stat(filepath.Join(e.uploads, "123456.jpg")); err != nil {
		t.Fatal("upload not in uploads dir")
	}

	u, _ := e.mc.PresignedGetObject(context.Background(), "smm", "course/123456.jpg", time.Minute, url.Values{})
	code, body, h := get(t, u.String())
	if code != 200 || !bytes.Equal(body, jpg) || h.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("GET after upload: %d ct=%s", code, h.Get("Content-Type"))
	}
}

func TestRangeUploadsOverridePreservedAndDeleteIsScoped(t *testing.T) {
	e := setup(t)
	data := []byte("0123456789")
	e.preserve(t, "1.bin", data)

	req, _ := http.NewRequest("GET", e.srv.URL+"/smm/course/1.bin", nil)
	req.Header.Set("Range", "bytes=2-5")
	r, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 206 || string(b) != "2345" {
		t.Fatalf("range: %d %q", r.StatusCode, b)
	}

	// PUT overrides the preserved copy for reads; DELETE removes only the upload
	put, _ := http.NewRequest("PUT", e.srv.URL+"/smm/course/1.bin", strings.NewReader("override"))
	if pr, err := http.DefaultClient.Do(put); err != nil || pr.StatusCode != 200 {
		t.Fatalf("put: %v", err)
	}
	if _, b, _ := get(t, e.srv.URL+"/smm/1.bin"); string(b) != "override" {
		t.Fatalf("after put: %q", b)
	}
	del, _ := http.NewRequest("DELETE", e.srv.URL+"/smm/1.bin", nil)
	http.DefaultClient.Do(del)
	if _, b, _ := get(t, e.srv.URL+"/smm/1.bin"); string(b) != "0123456789" {
		t.Fatalf("preserved copy damaged by delete: %q", b)
	}
}

func TestTraversalAndBadKeysCannotEscape(t *testing.T) {
	e := setup(t)
	secret := filepath.Join(filepath.Dir(e.preserved), "secret.bin")
	os.WriteFile(secret, []byte("nope"), 0o644)

	for _, p := range []string{"/smm/..%2Fsecret.bin", "/smm/%2e%2e/secret.bin", "/smm/..%5Csecret.bin", "/smm/x/..%2f..%2fsecret.bin"} {
		_, b, _ := get(t, e.srv.URL+p)
		if string(b) == "nope" {
			t.Fatalf("%s escaped the data dir", p)
		}
	}

	for k, want := range map[string]string{"smm/1.bin": "1.bin", "../../etc/passwd": "passwd", "..": "", "a/..": "", "bad name.bin": "", "": ""} {
		if got := s3local.KeyToName(k); got != want {
			t.Fatalf("KeyToName(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestPostWithoutFileAndOversize(t *testing.T) {
	e := setup(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("key", "x/1.bin")
	mw.Close()
	r, _ := http.Post(e.srv.URL+"/smm", mw.FormDataContentType(), &buf)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("post without file: %d", r.StatusCode)
	}

	small := httptest.NewServer(s3local.NewHandler(s3local.Config{UploadDir: filepath.Join(t.TempDir(), "u"), MaxUpload: 10}))
	defer small.Close()
	put, _ := http.NewRequest("PUT", small.URL+"/b/1.bin", strings.NewReader(strings.Repeat("x", 100)))
	pr, _ := http.DefaultClient.Do(put)
	pr.Body.Close()
	if pr.StatusCode != 413 {
		t.Fatalf("oversize put: %d", pr.StatusCode)
	}
}

// The Wii U client sends the upload as a plain "file" field without a filename; real S3 finds the file by field name.
func TestPostFilePartWithoutFilename(t *testing.T) {
	e := setup(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("key", "course/777.bin")

	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"`)
	fw, _ := mw.CreatePart(h)
	fw.Write([]byte("payload"))
	mw.Close()

	r, err := http.Post(e.srv.URL+"/smm", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()

	if r.StatusCode != 204 {
		t.Fatalf("post with a filename-less file part: %d", r.StatusCode)
	}

	if info, err := e.mc.StatObject(context.Background(), "smm", "course/777.bin", minio.StatObjectOptions{}); err != nil || info.Size != 7 {
		t.Fatalf("stat after upload: %v", err)
	}
}

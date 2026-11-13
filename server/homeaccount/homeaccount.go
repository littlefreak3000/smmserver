// Package homeaccount is a minimal stand-in for Nintendo's account server (NNAS) for a home setup.
//
// It implements just what Cemu's "Custom" network service calls (see Cemu src/Cemu/napi/napi_act.cpp):
//
//	POST /v1/api/oauth20/access_token/generate      NNID + password hash -> access token
//	GET  /v1/api/provider/nex_token/@me             -> game server host/port, NEX password, NEX token
//	GET  /v1/api/admin/mapped_ids                   NNID <-> PID
//	GET  /v1/api/provider/service_token/@me         independent service token
//
// There is no password check: anyone who can reach the port can log in as any NNID. That is the point for a
// LAN/home server, and why this must not be exposed to the internet.
//
// NEX tokens use the format the server validates (common_globals.DecryptToken): a big-endian CRC32 of the
// body followed by AES-256-CBC (zero IV, PKCS#7-style padding) of a little-endian struct.
package homeaccount

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DB            *sql.DB
	AESKey        []byte                           // PN_NEX_TOKEN_AES_KEY (32 bytes)
	Secret        []byte                           // signs OAuth tokens (PN_NEX_PASSWORD_SECRET is fine)
	Password      func(pid uint32) (string, error) // NEX password for a PID (globals.PasswordFromPID)
	NEXHost       string                           // host the console connects to for the NEX auth server (IPv4: Cemu limits it to 15 chars)
	NEXPort       int
	TokenLifetime time.Duration // NEX token lifetime (default 1h)
	SystemType    uint8         // 1 = Wii U (default), 2 = 3DS
	Logf          func(format string, args ...any)
	Now           func() time.Time
}

type nexToken struct { // must match common_globals.NEXToken byte for byte
	SystemType  uint8
	TokenType   uint8
	UserPID     uint32
	ExpireTime  uint64
	TitleID     uint64
	AccessLevel int8
}

const tokenTypeNEX = 3

// EncodeNEXToken builds the base64 token the server's login validator accepts.
func EncodeNEXToken(aesKey []byte, systemType uint8, pid uint32, titleID uint64, expire time.Time) (string, error) {
	var body bytes.Buffer
	if err := binary.Write(&body, binary.LittleEndian, nexToken{
		SystemType: systemType, TokenType: tokenTypeNEX, UserPID: pid,
		ExpireTime: uint64(expire.UnixMilli()), TitleID: titleID, AccessLevel: 0,
	}); err != nil {
		return "", err
	}

	plain := body.Bytes()
	checksum := crc32.ChecksumIEEE(plain)

	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}

	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(enc, padded)

	out := make([]byte, 4, 4+len(enc))
	binary.BigEndian.PutUint32(out, checksum)

	return base64.StdEncoding.EncodeToString(append(out, enc...)), nil
}

type Service struct {
	cfg Config
	mux *http.ServeMux
}

func New(cfg Config) *Service {
	if cfg.TokenLifetime == 0 {
		cfg.TokenLifetime = time.Hour
	}
	if cfg.SystemType == 0 {
		cfg.SystemType = 1
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	s := &Service{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/api/oauth20/access_token/generate", s.oauth)
	s.mux.HandleFunc("GET /v1/api/provider/nex_token/@me", s.nexToken)
	s.mux.HandleFunc("GET /v1/api/provider/service_token/@me", s.serviceToken)
	s.mux.HandleFunc("GET /v1/api/admin/mapped_ids", s.mappedIDs)

	return s
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (s *Service) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := &statusWriter{ResponseWriter: rw, status: 200}
	defer func() {
		s.cfg.Logf("account %s %s %s -> %d", r.RemoteAddr, r.Method, r.URL.RequestURI(), w.status)
	}()

	// * Anything we do not implement gets a clear XML error (and shows up in the log, which is how you
	// * discover what a client wants beyond these four endpoints).
	if _, pattern := s.mux.Handler(r); pattern == "" {
		writeError(w, 404, "0008", "Not Found")
		return
	}

	s.mux.ServeHTTP(w, r)
}

// ---- helpers ----------------------------------------------------------------

func esc(v string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(v))
	return b.String()
}

func writeXML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+"\n"+body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeXML(w, status, fmt.Sprintf(`<errors><error><code>%s</code><message>%s</message></error></errors>`, code, esc(msg)))
}

func (s *Service) sign(parts ...string) string {
	mac := hmac.New(sha256.New, s.cfg.Secret)
	mac.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// accessToken is stateless: v1.<base64url nnid>.<expires unix>.<hmac>, so restarts do not log anyone out.
func (s *Service) accessToken(kind, nnid string, lifetime time.Duration) string {
	exp := strconv.FormatInt(s.cfg.Now().Add(lifetime).Unix(), 10)
	n := base64.RawURLEncoding.EncodeToString([]byte(nnid))

	return strings.Join([]string{"v1", n, exp, s.sign(kind, n, exp)}, ".")
}

func (s *Service) verifyToken(kind, token string) (string, bool) {
	p := strings.Split(token, ".")
	if len(p) != 4 || p[0] != "v1" {
		return "", false
	}

	if !hmac.Equal([]byte(p[3]), []byte(s.sign(kind, p[1], p[2]))) {
		return "", false
	}

	if exp, err := strconv.ParseInt(p[2], 10, 64); err != nil || s.cfg.Now().Unix() >= exp {
		return "", false
	}

	nnid, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		return "", false
	}

	return string(nnid), true
}

func (s *Service) bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}

	return s.verifyToken("access", strings.TrimSpace(h[7:]))
}

func validNNID(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}

	for _, c := range n {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}

	return true
}

// ---- NNID <-> PID registry ---------------------------------------------------

// PIDFor returns the PID of an NNID, assigning one on first sight. The PID is derived from the NNID
// (stable even if the registry is lost) in 1,000,000,000..1,999,999,999, bumped on the rare collision.
func (s *Service) PIDFor(nnid string) (uint32, error) {
	lookup := func() (uint32, bool, error) {
		var pid uint32
		err := s.cfg.DB.QueryRow(`SELECT pid FROM datastore.accounts WHERE nnid = $1`, nnid).Scan(&pid)
		if err == sql.ErrNoRows {
			return 0, false, nil
		}

		return pid, err == nil, err
	}

	if pid, ok, err := lookup(); ok || err != nil {
		return pid, err
	}

	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(nnid)))
	pid := 1_000_000_000 + h.Sum32()%1_000_000_000

	for i := 0; i < 1000; i++ {
		_, err := s.cfg.DB.Exec(`INSERT INTO datastore.accounts (nnid, pid, created) VALUES ($1, $2, $3)`, nnid, pid, s.cfg.Now().UTC())
		if err == nil {
			s.cfg.Logf("account: registered %q as PID %d", nnid, pid)
			return pid, nil
		}

		if p, ok, lerr := lookup(); ok || lerr != nil { // lost a race for the same NNID
			return p, lerr
		}

		if !strings.Contains(err.Error(), "accounts.pid") {
			return 0, err
		}

		pid = 1_000_000_000 + (pid-1_000_000_000+1)%1_000_000_000
	}

	return 0, fmt.Errorf("could not allocate a PID for %q", nnid)
}

// NNIDFor is the reverse lookup.
func (s *Service) NNIDFor(pid uint32) (string, bool) {
	var nnid string
	if err := s.cfg.DB.QueryRow(`SELECT nnid FROM datastore.accounts WHERE pid = $1`, pid).Scan(&nnid); err != nil {
		return "", false
	}

	return nnid, true
}

// ---- endpoints ----------------------------------------------------------------

func (s *Service) oauth(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, 400, "0100", "Invalid request")
		return
	}

	var nnid string
	switch r.PostForm.Get("grant_type") {
	case "password":
		nnid = r.PostForm.Get("user_id") // the password hash is deliberately not checked (home server)
	case "refresh_token":
		var ok bool
		if nnid, ok = s.verifyToken("refresh", r.PostForm.Get("refresh_token")); !ok {
			writeError(w, 400, "0106", "Invalid refresh token")
			return
		}
	default:
		writeError(w, 400, "0100", "Invalid request")
		return
	}

	if !validNNID(nnid) {
		writeError(w, 400, "0100", "Invalid request")
		return
	}

	if _, err := s.PIDFor(nnid); err != nil { // register now so mapped_ids and nex_token agree
		s.cfg.Logf("account: %v", err)
		writeError(w, 500, "0008", "Internal error")
		return
	}

	const expires = time.Hour
	writeXML(w, 200, fmt.Sprintf(`<OAuth20><access_token><token>%s</token><refresh_token>%s</refresh_token><expires_in>%d</expires_in></access_token></OAuth20>`,
		s.accessToken("access", nnid, expires), s.accessToken("refresh", nnid, 30*24*time.Hour), int(expires.Seconds())))
}

func (s *Service) nexToken(w http.ResponseWriter, r *http.Request) {
	nnid, ok := s.bearer(r)
	if !ok {
		writeError(w, 400, "0002", "Invalid access token")
		return
	}

	gameServerID := r.URL.Query().Get("game_server_id")
	if gameServerID == "" {
		writeError(w, 200, "0118", "Unique ID and Game Server ID are not linked")
		return
	}

	pid, err := s.PIDFor(nnid)
	if err != nil {
		s.cfg.Logf("account: %v", err)
		writeError(w, 500, "0008", "Internal error")
		return
	}

	password, err := s.cfg.Password(pid)
	if err != nil {
		s.cfg.Logf("account: %v", err)
		writeError(w, 500, "0008", "Internal error")
		return
	}

	titleID, _ := strconv.ParseUint(strings.TrimPrefix(r.Header.Get("X-Nintendo-Title-ID"), "0x"), 16, 64)

	token, err := EncodeNEXToken(s.cfg.AESKey, s.cfg.SystemType, pid, titleID, s.cfg.Now().Add(s.cfg.TokenLifetime))
	if err != nil {
		s.cfg.Logf("account: %v", err)
		writeError(w, 500, "0008", "Internal error")
		return
	}

	// * This single server serves every game_server_id it is asked about (SMM has one per region).
	s.cfg.Logf("account: NEX token for %q (PID %d), game_server_id=%s title=%016x", nnid, pid, gameServerID, titleID)

	writeXML(w, 200, fmt.Sprintf(`<nex_token><host>%s</host><nex_password>%s</nex_password><pid>%d</pid><port>%d</port><token>%s</token></nex_token>`,
		esc(s.cfg.NEXHost), esc(password), pid, s.cfg.NEXPort, token))
}

func (s *Service) serviceToken(w http.ResponseWriter, r *http.Request) {
	nnid, ok := s.bearer(r)
	if !ok {
		writeError(w, 400, "0002", "Invalid access token")
		return
	}

	if r.URL.Query().Get("client_id") == "" {
		writeError(w, 200, "1021", "The requested game server was not found")
		return
	}

	// * There are no independent services (Dothub etc.) behind this; hand out an opaque token so a client
	// * that insists on having one does not abort.
	var nonce [8]byte
	rand.Read(nonce[:])
	writeXML(w, 200, fmt.Sprintf(`<service_token><token>%s</token></service_token>`,
		s.accessToken("service", nnid, 24*time.Hour)+"."+hex.EncodeToString(nonce[:])))
}

func (s *Service) mappedIDs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.bearer(r); !ok {
		writeError(w, 400, "0002", "Invalid access token")
		return
	}

	q := r.URL.Query()
	in, out := q.Get("input_type"), q.Get("output_type")

	var b strings.Builder
	b.WriteString("<mapped_ids>")
	for _, input := range strings.Split(q.Get("input"), ",") {
		if input == "" {
			continue
		}

		outID := ""
		switch {
		case in == "user_id" && out == "pid":
			if validNNID(input) {
				if pid, err := s.PIDFor(input); err == nil {
					outID = strconv.FormatUint(uint64(pid), 10)
				}
			}
		case in == "pid" && out == "user_id":
			if n, err := strconv.ParseUint(input, 10, 32); err == nil {
				outID, _ = s.NNIDFor(uint32(n))
			}
		}

		fmt.Fprintf(&b, `<mapped_id><in_id>%s</in_id><out_id>%s</out_id></mapped_id>`, esc(input), esc(outID))
	}
	b.WriteString("</mapped_ids>")

	writeXML(w, 200, b.String())
}

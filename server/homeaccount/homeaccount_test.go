package homeaccount_test

// The tests replay the requests Cemu makes (headers and form fields copied from Cemu's napi_act.cpp) and hand
// the issued token to the SERVER'S OWN validator (common_globals.ValidatePretendoLoginData / DecryptToken).

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/xml"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	ticket_granting_types "github.com/PretendoNetwork/nex-protocols-go/v2/ticket-granting/types"
	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/homeaccount"
)

type env struct {
	db     *sql.DB
	srv    *httptest.Server
	svc    *homeaccount.Service
	aesKey []byte
	now    time.Time
}

func setup(t *testing.T) *env {
	t.Helper()

	logger := plogger.NewLogger()
	globals.Logger = logger
	common_globals.Logger = logger

	e := &env{aesKey: make([]byte, 32), now: time.Now()}
	rand.Read(e.aesKey)
	globals.NEXPasswordSecret = make([]byte, 32)
	rand.Read(globals.NEXPasswordSecret)

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e.db = db
	e.svc = homeaccount.New(homeaccount.Config{
		DB:      db,
		AESKey:  e.aesKey,
		Secret:  globals.NEXPasswordSecret,
		NEXHost: "192.168.1.50",
		NEXPort: 60000,
		Password: func(pid uint32) (string, error) {
			p, code := globals.PasswordFromPID(types.NewPID(uint64(pid)))
			if code != 0 {
				return "", os.ErrInvalid
			}
			return p, nil
		},
		Now: func() time.Time { return e.now },
	})
	e.srv = httptest.NewServer(e.svc)
	t.Cleanup(e.srv.Close)

	return e
}

type errorsXML struct {
	Code    string `xml:"error>code"`
	Message string `xml:"error>message"`
}

func (e *env) do(t *testing.T, method, path string, headers map[string]string, form url.Values) (int, string) {
	t.Helper()

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

// oauth replays Cemu's ACT_GetOauthToken request.
func (e *env) oauth(t *testing.T, nnid string) string {
	t.Helper()

	code, body := e.do(t, "POST", "/v1/api/oauth20/access_token/generate", map[string]string{
		"X-Nintendo-Client-ID": "a2efa818a34fa16b8afbc8a74eba3eda", "X-Nintendo-Title-ID": "000500001018db00",
	}, url.Values{"grant_type": {"password"}, "user_id": {nnid}, "password": {strings.Repeat("ab", 32)}, "password_type": {"hash"}})
	if code != 200 {
		t.Fatalf("oauth: %d %s", code, body)
	}

	var r struct {
		Token   string `xml:"access_token>token"`
		Refresh string `xml:"access_token>refresh_token"`
		Expires int    `xml:"access_token>expires_in"`
	}
	if err := xml.Unmarshal([]byte(body), &r); err != nil || r.Token == "" || r.Refresh == "" || r.Expires != 3600 {
		t.Fatalf("oauth response: %v %+v\n%s", err, r, body)
	}

	return r.Token
}

type nexTokenXML struct {
	Host     string `xml:"host"`
	Password string `xml:"nex_password"`
	PID      uint32 `xml:"pid"`
	Port     int    `xml:"port"`
	Token    string `xml:"token"`
}

// nexToken replays Cemu's ACT_GetNexToken request.
func (e *env) nexToken(t *testing.T, bearer, titleID string) nexTokenXML {
	t.Helper()

	code, body := e.do(t, "GET", "/v1/api/provider/nex_token/@me?game_server_id=1018DB00", map[string]string{
		"Authorization": "Bearer " + bearer, "X-Nintendo-Title-ID": titleID,
	}, nil)
	if code != 200 {
		t.Fatalf("nex_token: %d %s", code, body)
	}

	var r nexTokenXML
	if err := xml.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("nex_token xml: %v\n%s", err, body)
	}

	return r
}

func (e *env) mappedPID(t *testing.T, bearer, nnid string) string {
	t.Helper()

	code, body := e.do(t, "GET", "/v1/api/admin/mapped_ids?input_type=user_id&output_type=pid&input="+url.QueryEscape(nnid),
		map[string]string{"Authorization": "Bearer " + bearer}, nil)
	if code != 200 {
		t.Fatalf("mapped_ids: %d %s", code, body)
	}

	var r struct {
		Out string `xml:"mapped_id>out_id"`
	}
	xml.Unmarshal([]byte(body), &r)

	return r.Out
}

func loginData(token string) types.DataHolder {
	nl := ticket_granting_types.NewNintendoLoginData()
	nl.Token = types.NewString(token)
	h := types.NewDataHolder()
	h.Object = nl

	return h
}

func TestFullCemuFlowProducesATokenTheServerAccepts(t *testing.T) {
	e := setup(t)

	access := e.oauth(t, "HomePlayer1")
	pidStr := e.mappedPID(t, access, "HomePlayer1")
	pid, err := strconv.ParseUint(pidStr, 10, 32)
	if err != nil || pid < 1_000_000_000 || pid >= 2_000_000_000 {
		t.Fatalf("mapped pid %q", pidStr)
	}

	n := e.nexToken(t, access, "000500001018db00")

	// * the fields Cemu copies, with Cemu's size limits
	if n.Host != "192.168.1.50" || n.Port != 60000 || uint64(n.PID) != pid {
		t.Fatalf("nex_token = %+v", n)
	}
	if len(n.Host) > 15 || len(n.Password) > 64 || len(n.Token) > 512 || n.Token == "" {
		t.Fatalf("field sizes: host %d password %d token %d", len(n.Host), len(n.Password), len(n.Token))
	}

	// * the password must be what the server derives for this PID (it is the Kerberos secret)
	want, _ := globals.PasswordFromPID(types.NewPID(pid))
	if n.Password != want {
		t.Fatalf("nex_password %q != server's %q", n.Password, want)
	}

	// * the server's own decoder
	raw, err := base64.StdEncoding.DecodeString(n.Token)
	if err != nil {
		t.Fatal(err)
	}
	tok, nexErr := common_globals.DecryptToken(raw, e.aesKey)
	if nexErr != nil {
		t.Fatal(nexErr.Message)
	}
	if uint64(tok.UserPID) != pid || tok.TokenType != 3 || tok.TitleID != 0x000500001018db00 || tok.SystemType != 1 || tok.AccessLevel != 0 {
		t.Fatalf("token = %+v", tok)
	}
	if d := time.UnixMilli(int64(tok.ExpireTime)).Sub(e.now); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("token lifetime %v", d)
	}

	// * the server's own login validation, as run during NEX Login
	if err := common_globals.ValidatePretendoLoginData(types.NewPID(pid), loginData(n.Token), e.aesKey); err != nil {
		t.Fatalf("validator rejected the token: %s", err.Message)
	}
	if err := common_globals.ValidatePretendoLoginData(types.NewPID(pid+1), loginData(n.Token), e.aesKey); err == nil || !strings.Contains(err.Error(), "PrincipalIDUnmatched") {
		t.Fatalf("a token for another PID must be rejected, got %v", err)
	}
	wrongKey := make([]byte, 32)
	if err := common_globals.ValidatePretendoLoginData(types.NewPID(pid), loginData(n.Token), wrongKey); err == nil {
		t.Fatal("a token under another key must be rejected")
	}

	e.now = e.now.Add(2 * time.Hour) // the validator uses the real clock; make the token genuinely old instead
	old, _ := homeaccount.EncodeNEXToken(e.aesKey, 1, uint32(pid), 0, time.Now().Add(-time.Minute))
	if err := common_globals.ValidatePretendoLoginData(types.NewPID(pid), loginData(old), e.aesKey); err == nil || !strings.Contains(err.Error(), "TokenExpired") {
		t.Fatalf("an expired token must be rejected, got %v", err)
	}
}

func TestEveryDeviceStyleOfLoginDataIsAccepted(t *testing.T) {
	e := setup(t)
	n := e.nexToken(t, e.oauth(t, "Player3DS"), "0004000000000000")

	// * 3DS / AuthenticationInfo send the base64 with ".", "-", "*" instead of "+", "/", "=" (see the validator)
	r := strings.NewReplacer("+", ".", "/", "-", "=", "*")
	ai := ticket_granting_types.NewAuthenticationInfo()
	ai.Token = types.NewString(r.Replace(n.Token))
	h := types.NewDataHolder()
	h.Object = ai

	if err := common_globals.ValidatePretendoLoginData(types.NewPID(uint64(n.PID)), h, e.aesKey); err != nil {
		t.Fatalf("AuthenticationInfo login rejected: %s", err.Message)
	}
}

func TestPIDsAreStableCaseInsensitiveAndReversible(t *testing.T) {
	e := setup(t)
	a := e.oauth(t, "Alice_Smith")

	p1 := e.mappedPID(t, a, "Alice_Smith")
	if p2 := e.mappedPID(t, a, "alice_smith"); p2 != p1 {
		t.Fatalf("case changed the PID: %s vs %s", p1, p2)
	}
	if other := e.mappedPID(t, a, "Bob_Jones"); other == p1 || other == "" {
		t.Fatalf("different NNIDs share a PID: %s", other)
	}

	// * reverse lookup, several inputs at once, unknown PID gives an empty out_id (Cemu treats that as "not found")
	_, body := e.do(t, "GET", "/v1/api/admin/mapped_ids?input_type=pid&output_type=user_id&input="+p1+",1999999999",
		map[string]string{"Authorization": "Bearer " + a}, nil)
	if !strings.Contains(body, "<out_id>Alice_Smith</out_id>") || !strings.Contains(body, "<in_id>1999999999</in_id><out_id></out_id>") {
		t.Fatalf("reverse lookup:\n%s", body)
	}

	// * a fresh service on the same registry (restart) hands out the same PID
	if again := e.mappedPID(t, e.oauth(t, "ALICE_SMITH"), "Alice_Smith"); again != p1 {
		t.Fatalf("PID changed: %s vs %s", again, p1)
	}
}

func TestClaimingAnImportedCreatorsPID(t *testing.T) {
	e := setup(t)
	a := e.oauth(t, "ClaimMe")
	if before := e.mappedPID(t, a, "ClaimMe"); before == "1424937128" {
		t.Fatal("test premise broken")
	}

	// * point the NNID at an owner PID from an imported archive, so those levels become "yours"
	if res, err := e.db.Exec(`UPDATE datastore.accounts SET pid = 1424937128 WHERE nnid = $1`, "ClaimMe"); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("claim updated %d rows", n)
	}

	if got := e.mappedPID(t, a, "claimme"); got != "1424937128" {
		t.Fatalf("mapped_ids after claim = %s", got)
	}

	n := e.nexToken(t, a, "000500001018db00")
	if n.PID != 1424937128 {
		t.Fatalf("nex_token pid = %d", n.PID)
	}
	want, _ := globals.PasswordFromPID(types.NewPID(1424937128))
	if n.Password != want {
		t.Fatal("password was not derived for the claimed PID")
	}
	if err := common_globals.ValidatePretendoLoginData(types.NewPID(1424937128), loginData(n.Token), e.aesKey); err != nil {
		t.Fatalf("validator rejected the claimed-PID token: %s", err.Message)
	}
}

func TestPIDCollisionIsBumped(t *testing.T) {
	e := setup(t)

	// * the natural PID of "Victim" (same derivation as the service), occupied by someone else
	h := fnv.New32a()
	h.Write([]byte("victim"))
	natural := 1_000_000_000 + h.Sum32()%1_000_000_000
	if _, err := e.db.Exec(`INSERT INTO datastore.accounts (nnid, pid, created) VALUES ($1, $2, $3)`, "Squatter", natural, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// * and the next one too
	if _, err := e.db.Exec(`INSERT INTO datastore.accounts (nnid, pid, created) VALUES ($1, $2, $3)`, "Squatter2", natural+1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	got := e.mappedPID(t, e.oauth(t, "Victim"), "Victim")
	if got != strconv.FormatUint(uint64(natural)+2, 10) {
		t.Fatalf("Victim got %s, want %d", got, natural+2)
	}

	// * and it stays put
	if again := e.mappedPID(t, e.oauth(t, "VICTIM"), "victim"); again != got {
		t.Fatalf("PID moved: %s -> %s", got, again)
	}
	if n, ok := e.svc.NNIDFor(natural); !ok || n != "Squatter" {
		t.Fatalf("squatter lost its PID: %q %v", n, ok)
	}
}

func TestRefreshTokenAndTamperedAndExpiredTokens(t *testing.T) {
	e := setup(t)

	// * refresh_token grant
	_, body := e.do(t, "POST", "/v1/api/oauth20/access_token/generate", nil,
		url.Values{"grant_type": {"password"}, "user_id": {"RefreshMe"}, "password": {"x"}, "password_type": {"hash"}})
	var r struct {
		Token   string `xml:"access_token>token"`
		Refresh string `xml:"access_token>refresh_token"`
	}
	xml.Unmarshal([]byte(body), &r)

	code, body := e.do(t, "POST", "/v1/api/oauth20/access_token/generate", nil, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.Refresh}})
	if code != 200 || !strings.Contains(body, "<token>") {
		t.Fatalf("refresh: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/v1/api/oauth20/access_token/generate", nil, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.Token}}); code != 400 {
		t.Fatalf("an access token must not work as a refresh token: %d", code)
	}

	check := func(name, auth string, wantCode int, wantErr string) {
		t.Helper()
		c, b := e.do(t, "GET", "/v1/api/provider/nex_token/@me?game_server_id=1018DB00", map[string]string{"Authorization": auth}, nil)
		var er errorsXML
		xml.Unmarshal([]byte(b), &er)
		if c != wantCode || er.Code != wantErr {
			t.Fatalf("%s: %d code=%q\n%s", name, c, er.Code, b)
		}
	}

	check("no auth", "", 400, "0002")
	check("garbage", "Bearer nonsense", 400, "0002")
	parts := strings.Split(r.Token, ".")
	parts[2] = "99999999999"
	check("tampered expiry", "Bearer "+strings.Join(parts, "."), 400, "0002")
	check("refresh used as access", "Bearer "+r.Refresh, 400, "0002")

	e.now = e.now.Add(2 * time.Hour)
	check("expired", "Bearer "+r.Token, 400, "0002")
}

func TestBadRequestsAndUnknownPathsGetXMLErrors(t *testing.T) {
	e := setup(t)

	for _, form := range []url.Values{
		{"grant_type": {"client_credentials"}},
		{"grant_type": {"password"}},
		{"grant_type": {"password"}, "user_id": {"has space"}},
	} {
		code, body := e.do(t, "POST", "/v1/api/oauth20/access_token/generate", nil, form)
		var er errorsXML
		xml.Unmarshal([]byte(body), &er)
		if code != 400 || er.Code != "0100" {
			t.Fatalf("form %v: %d %s", form, code, body)
		}
	}

	access := e.oauth(t, "NoGameServer")
	_, body := e.do(t, "GET", "/v1/api/provider/nex_token/@me", map[string]string{"Authorization": "Bearer " + access}, nil)
	if !strings.Contains(body, "<code>0118</code>") {
		t.Fatalf("missing game_server_id: %s", body)
	}

	code, body := e.do(t, "GET", "/v1/api/people/@me/profile", map[string]string{"Authorization": "Bearer " + access}, nil)
	if code != 404 || !strings.Contains(body, "<errors>") {
		t.Fatalf("unknown path: %d %s", code, body)
	}

	code, body = e.do(t, "GET", "/v1/api/provider/service_token/@me?client_id=abc", map[string]string{"Authorization": "Bearer " + access}, nil)
	if code != 200 || !strings.Contains(body, "<service_token><token>") {
		t.Fatalf("service token: %d %s", code, body)
	}
}

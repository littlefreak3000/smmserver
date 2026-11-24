package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	"github.com/PretendoNetwork/super-mario-maker/consoleproxy"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/homeaccount"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/s3local"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
	"github.com/joho/godotenv"
)

// Home/archive mode: no Postgres and no external S3.
//
//	SMM_DATA_DIR      data folder (default ./data)
//	  catalog.sqlite  the level catalog
//	  courses/        read-only archive: <id>.bin, <id>.jpg, 900000.bin
//	  uploads/        new uploads from consoles
//	SMM_PUBLIC_HOST   address the CONSOLE uses to reach this machine (default: first LAN IPv4)
//	SMM_S3_PORT       port of the built-in S3 (default 9000)
//
//	SMM_FETCH         fetch archived levels on demand from the public archive (default 1; 0 = fully offline)
//	SMM_POOL_TARGET   levels to keep in each of 4 clear-rate bands, fetched in the background (default 25; 0 = none)
//	SMM_POOL_GROW_EVERY / _GROW_BY / SMM_POOL_MAX  slow growth: every 10m add 1 level per band, up to 250 per band (0 = stop)
//	SMM_HISTORY_LEVELS  how many of each player's most recently played levels to remember so lists skip them (default 1000; off = none)
//	SMM_FETCH_API / SMM_FETCH_WAYBACK  override the two upstream services
//	SMM_BIND_HOST     bind the HTTP services (S3, account) to one address, e.g. 127.0.0.1 (default: all; the NEX UDP ports always bind all)
//	SMM_ACCOUNT_PORT  port of the built-in account service for Cemu (default 8080, 0 = off)
//	secrets.json      generated on first run if PN_NEX_TOKEN_AES_KEY / PN_NEX_PASSWORD_SECRET are not set
//	network_services.xml  written for Cemu: copy it next to Cemu's settings.xml
//
// Defaults for the NEX ports: authentication 60000, secure 60001 (UDP).
//
// If PN_SMM_CONFIG_S3_ENDPOINT is set, the built-in S3 is not started and that S3 is used instead.

var catalogPath string

// publicHost is the address consoles use to reach this machine.
var publicHost string

// proxyPort is the port the Wii U proxy listens on ("" when it is off or could not start).
var proxyPort string

var dataDirPath string

// coursesDirPath is where course files live; builtinS3 is true when the built-in S3 serves it.
var coursesDirPath string
var builtinS3 bool

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func setupLocalStorage() {
	dataDir := getenv("SMM_DATA_DIR", "./data")
	coursesDir := filepath.Join(dataDir, "courses")
	coursesDirPath = coursesDir
	uploadsDir := filepath.Join(dataDir, "uploads")
	catalogPath = filepath.Join(dataDir, "catalog.sqlite")
	dataDirPath = dataDir

	for _, d := range []string{coursesDir, uploadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			globals.Logger.Criticalf("Cannot create %s: %s", d, err.Error())
			os.Exit(1)
		}
	}

	loadSettingsFile(dataDir)
	publicHost = getenv("SMM_PUBLIC_HOST", lanIPv4())

	ensureSecrets(dataDir)
	setDefaultEnv("PN_SMM_AUTHENTICATION_SERVER_PORT", "60000")
	setDefaultEnv("PN_SMM_SECURE_SERVER_HOST", publicHost)
	setDefaultEnv("PN_SMM_SECURE_SERVER_PORT", "60001")
	preflightUDP()

	if os.Getenv("PN_SMM_CONFIG_S3_ENDPOINT") != "" {
		globals.Logger.Info("Using external S3 from PN_SMM_CONFIG_S3_ENDPOINT")
		return
	}

	// * Course World will not open without the event course metadata file. We have no real copy,
	// * so create an empty placeholder (the server can boot, but Course World may not work).
	event := filepath.Join(coursesDir, "900000.bin")
	if _, err := os.Stat(event); os.IsNotExist(err) {
		if err := os.WriteFile(event, nil, 0o644); err != nil {
			globals.Logger.Criticalf("Cannot create placeholder %s: %s", event, err.Error())
			os.Exit(1)
		}

		globals.Logger.Warningf("%s did not exist; created an EMPTY placeholder. Course World may not open until you supply the real file", event)
	}

	port := getenv("SMM_S3_PORT", "9000")
	host := publicHost

	ln, err := s3local.Listen(net.JoinHostPort(os.Getenv("SMM_BIND_HOST"), port), s3local.Config{
		PreservedDir: coursesDir,
		UploadDir:    uploadsDir,
		Logf:         func(f string, a ...any) { globals.Tracef(f, a...) },
	})
	if err != nil {
		globals.Logger.Criticalf("Cannot start the built-in S3 on port %s: %s. Another program is probably using that port; change it with SMM_S3_PORT in %s", port, err.Error(), filepath.Join(dataDirPath, "settings.env"))
		os.Exit(1)
	}

	builtinS3 = true
	globals.Logger.Successf("Built-in S3 listening on %s, courses in %s", ln.Addr().String(), coursesDir)
	globals.Logger.Infof("Consoles will download courses from http://%s:%s (set SMM_PUBLIC_HOST if that is not reachable from the console)", host, port)

	// * The rest of the server (and the minio client library) reads these.
	os.Setenv("PN_SMM_CONFIG_S3_ENDPOINT", host+":"+port)
	os.Setenv("PN_SMM_CONFIG_S3_SECURE", "false")
	setDefaultEnv("PN_SMM_CONFIG_S3_ACCESS_KEY", "local")
	setDefaultEnv("PN_SMM_CONFIG_S3_ACCESS_SECRET", "local")
	setDefaultEnv("PN_SMM_CONFIG_S3_BUCKET", "smm")
	setDefaultEnv("PN_SMM_CONFIG_S3_KEY_BASE", "course")
}

// lanIPv4 returns the address this PC uses to reach the local network. Asking the OS which source address it would
// use for an outgoing packet picks the real adapter even when VPN, WSL or virtual-machine adapters exist (no packet is
// sent: connecting a UDP socket only selects a route). It falls back to the first private IPv4 address.
func lanIPv4() string {
	if c, err := net.Dial("udp", "192.0.2.1:9"); err == nil { // TEST-NET-1: never routed anywhere
		defer c.Close()

		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && n.IP.IsPrivate() {
				return n.IP.String()
			}
		}
	}

	return "127.0.0.1"
}

// otherLANIPv4 lists the private IPv4 addresses of this PC other than `except`: with several adapters (Wi-Fi and cable, a
// VPN, a virtual machine) the one picked may not be the one the console can reach, so the start-up box shows the rest.
func otherLANIPv4(except string) []string {
	var out []string

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	for _, ifc := range ifaces {
		// * a loopback adapter (even one with a private address on it) or one that is down cannot be reached from the console
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}

		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && n.IP.String() != except {
				out = append(out, n.IP.String())
			}
		}
	}

	return out
}

func setDefaultEnv(k, v string) {
	if os.Getenv(k) == "" {
		os.Setenv(k, v)
	}
}

// ensureSecrets makes the home setup work out of the box: the account service and the NEX server run in the
// same process, so they only need to agree on two random secrets, which are kept in <data>/secrets.json.
func ensureSecrets(dataDir string) {
	if os.Getenv("PN_NEX_TOKEN_AES_KEY") != "" && os.Getenv("PN_NEX_PASSWORD_SECRET") != "" {
		return
	}

	path := filepath.Join(dataDir, "secrets.json")

	var secrets struct {
		AESKey         string `json:"aes_key"`
		PasswordSecret string `json:"password_secret"`
	}

	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &secrets); err != nil {
			globals.Logger.Criticalf("%s is not valid JSON: %s", path, err.Error())
			os.Exit(1)
		}
	} else {
		random := func() string {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				globals.Logger.Criticalf("Cannot generate secrets: %s", err.Error())
				os.Exit(1)
			}

			return hex.EncodeToString(b)
		}

		secrets.AESKey, secrets.PasswordSecret = random(), random()

		b, _ := json.MarshalIndent(secrets, "", "  ")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			globals.Logger.Criticalf("Cannot write %s: %s", path, err.Error())
			os.Exit(1)
		}

		globals.Logger.Infof("Generated new secrets in %s (keep this file with the archive: changing it logs everyone out)", path)
	}

	setDefaultEnv("PN_NEX_TOKEN_AES_KEY", secrets.AESKey)
	setDefaultEnv("PN_NEX_PASSWORD_SECRET", secrets.PasswordSecret)
}

// startHomeAccount runs the built-in account service (homeaccount) that Cemu logs in to, and writes the
// network_services.xml that points Cemu at it. Needs the catalog open and the secrets decoded.
func startHomeAccount() {
	port := getenv("SMM_ACCOUNT_PORT", "8080")
	if port == "0" {
		return
	}

	authPort, _ := strconv.Atoi(os.Getenv("PN_SMM_AUTHENTICATION_SERVER_PORT"))

	svc := homeaccount.New(homeaccount.Config{
		DB:      database.DB,
		AESKey:  globals.NEXTokenAESKey,
		Secret:  globals.NEXPasswordSecret,
		NEXHost: publicHost,
		NEXPort: authPort,
		Password: func(pid uint32) (string, error) {
			password, code := globals.PasswordFromPID(types.NewPID(uint64(pid)))
			if code != 0 {
				return "", fmt.Errorf("cannot derive a NEX password for PID %d", pid)
			}

			return password, nil
		},
		Logf: func(f string, a ...any) { globals.Logger.Infof(f, a...) },
		// * SMM_ACCOUNT_SYSTEM_TITLES=1: answer the console's system programs too (the default drops them; see homeaccount.Config)
		AnswerSystemTitles: getenv("SMM_ACCOUNT_SYSTEM_TITLES", "") == "1",
	})

	ln, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("SMM_BIND_HOST"), port))
	if err != nil {
		globals.Logger.Criticalf("Cannot start the account service on port %s: %s. Another program is probably using that port; change it with SMM_ACCOUNT_PORT in %s", port, err.Error(), filepath.Join(dataDirPath, "settings.env"))
		os.Exit(1)
	}

	handler := mountSeedPage(svc)

	go http.Serve(ln, handler) //nolint:errcheck

	startConsoleProxy(handler, func(name string, pid uint32) {
		if _, _, err := svc.LearnPID(name, pid); err != nil {
			globals.Logger.Warningf("Cannot use the player ID %d that Pretendo reports for %q: %s", pid, name, err.Error())
		}
	})

	url := "http://" + publicHost + ":" + port
	seedPageURL = url + "/seed"
	xmlPath := filepath.Join(dataDirPath, "network_services.xml")
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<content>
	<networkname>Home SMM</networkname>
	<disablesslverification>1</disablesslverification>
	<urls>
		<act>%s</act>
	</urls>
</content>
`, url)

	if err := os.WriteFile(xmlPath, []byte(xml), 0o644); err != nil {
		globals.Logger.Warningf("Cannot write %s: %s", xmlPath, err.Error())
	}

	globals.Logger.Successf("Account service for Cemu listening on %s (no passwords are checked: LAN only!)", ln.Addr().String())
	globals.Logger.Infof("Cemu: copy %s next to Cemu's settings.xml, then Options > General settings > Account > Network Service: Custom", xmlPath)

	if len(publicHost) > 15 {
		globals.Logger.Warningf("SMM_PUBLIC_HOST %q is longer than 15 characters; Cemu cannot use it as the NEX host. Use an IPv4 address", publicHost)
	}
}

// startConsoleProxy starts the proxy a Wii U patched with Inkay uses to reach this server (see docs/setup-wiiu.md).
// SMM_PROXY_PORT=0 turns it off. It only answers the account service, the connection test and the course downloads.
func startConsoleProxy(account http.Handler, learn func(userID string, pid uint32)) {
	port := getenv("SMM_PROXY_PORT", "8888")
	if port == "0" {
		return
	}

	p, err := consoleproxy.New(consoleproxy.Config{
		Account: account,
		S3:      "127.0.0.1:" + getenv("SMM_S3_PORT", "9000"),
		S3Port:  getenv("SMM_S3_PORT", "9000"),
		Logf:    func(f string, a ...any) { globals.Tracef(f, a...) },
		// * SMM_PROXY_SPOTPASS=1 (diagnostics only): let SpotPass connections in and log what they ask
		ServeSpotPass: getenv("SMM_PROXY_SPOTPASS", "") == "1",
		// * SMM_PROXY_ANSWER=1: answer hosts the server does not serve with HTTP errors instead of dropping the connection
		AnswerRefusals: getenv("SMM_PROXY_ANSWER", "") == "1",
		// * SMM_PROXY_DROP_ALL=1 (diagnostics only): reset every connection to the proxy port
		DropAll: getenv("SMM_PROXY_DROP_ALL", "") == "1",
		// * SMM_PROXY_PRETENDO=1: the console's own programs keep using the real Pretendo Network through the proxy (needs a
		// * Pretendo account on the console); only games are answered here
		Pretendo: getenv("SMM_PROXY_PRETENDO", "") == "1",
		// * in pass-through mode the real player ID of each account is read from Pretendo's profile reply
		LearnProfile: learn,
	})
	if err != nil {
		globals.Logger.Warningf("Cannot set up the Wii U proxy: %s", err.Error())

		return
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("SMM_BIND_HOST"), port))
	if err != nil {
		globals.Logger.Warningf("Cannot start the Wii U proxy on port %s (%s). Only needed for a real Wii U; change it with SMM_PROXY_PORT", port, err.Error())

		return
	}

	go http.Serve(ln, p) //nolint:errcheck

	proxyPort = port

	globals.Logger.Infof("Wii U proxy listening on %s (only for a Wii U patched with Inkay; SMM_PROXY_PORT=0 turns it off)", ln.Addr().String())
}

// cacheLimitMB reads SMM_CACHE_MAX_MB: how many MB the downloaded level files may take before the oldest are removed.
// 0 (the default) means no limit; a very small number is raised, since the pool keeps about 100 levels in stock.
func cacheLimitMB() int {
	raw := getenv("SMM_CACHE_MAX_MB", "0")

	mb, err := strconv.Atoi(raw)
	if err != nil || mb < 0 {
		globals.Logger.Warningf("SMM_CACHE_MAX_MB must be a number of megabytes (0 = no limit); ignoring %q", raw)

		return 0
	}

	if mb > 0 && mb < 20 {
		globals.Logger.Warningf("SMM_CACHE_MAX_MB=%d is too small to hold the levels the pool keeps in stock; using 20", mb)

		return 20
	}

	return mb
}

// cacheManifest is the cache.json of the data folder, if there is one (see docs/cache.md).
var cacheManifest *pool.CacheManifest

func loadCacheManifest() *pool.CacheManifest {
	m, err := pool.ReadManifest(dataDirPath)
	if err != nil {
		globals.Logger.Warningf("Cannot read the cache manifest: %s", err.Error())

		return nil
	}

	cacheManifest = m

	return m
}

// applyDifficultyPrior sets how levels are sorted by difficulty from SMM_DIFFICULTY_PRIOR (see package difficulty and
// docs/difficulty.md). A locked cache keeps the prior it was built with, so every copy of it sorts the levels the same way.
// It must run before anything imports a level.
func applyDifficultyPrior() {
	prior, err := difficulty.Parse(getenv("SMM_DIFFICULTY_PRIOR", ""))
	if err != nil {
		globals.Logger.Warningf("%s; using the default %s", err.Error(), difficulty.Default.String())

		prior = difficulty.Default
	}

	if m := loadCacheManifest(); m != nil && m.Locked {
		if p, err := pool.ManifestPrior(m); err == nil {
			if p != prior {
				globals.Logger.Infof("Cache %s was built with the difficulty prior %s; using that and not %s, so every copy sorts levels alike", cacheLabel(m), p.String(), prior.String())
			}

			prior = p
		}
	}

	difficulty.Current = prior

	if prior.Strength == 0 {
		globals.Logger.Info("Difficulty: levels are sorted by their raw clear rate (SMM_DIFFICULTY_PRIOR=off)")
	} else {
		globals.Logger.Infof("Difficulty: levels with few attempts are pulled towards a typical level (prior %s: clear rate %.0f%%, worth %g attempts; SMM_DIFFICULTY_PRIOR=off for raw clear rates)", prior.String(), prior.Mean*100, prior.Strength)
	}
}

func cacheLabel(m *pool.CacheManifest) string {
	if m.Seed != "" {
		return fmt.Sprintf("%q (fingerprint %s)", m.Seed, m.Fingerprint)
	}

	return fmt.Sprintf("of random levels (fingerprint %s)", m.Fingerprint)
}

// startFetching enables on-demand fetching from the public archive: a background pool that keeps every clear-rate
// band stocked, and a hook that fetches a level when the game asks for an ID the catalog does not have. Needs the
// catalog open and the built-in S3 (the downloaded files must land in the folder the S3 serves).
func startFetching() {
	if m := loadCacheManifest(); m != nil && m.Locked {
		globals.Logger.Infof("Cache %s: the set of levels is fixed (%d levels), so fetching is off and nothing is added. `smm-server cache unlock` changes that", cacheLabel(m), m.Levels)

		return
	}

	if getenv("SMM_FETCH", "1") == "0" {
		globals.Logger.Info("On-demand level fetching is off (SMM_FETCH=0); only levels already in the archive folder are available")
		return
	}

	if !builtinS3 {
		globals.Logger.Info("On-demand level fetching needs the built-in S3; it is off while an external S3 is configured")
		return
	}

	api := getenv("SMM_FETCH_API", upstream.DefaultAPI)
	wayback := getenv("SMM_FETCH_WAYBACK", upstream.DefaultWayback)

	client := upstream.New(api, wayback, 0, 0)
	client.Logf = func(f string, a ...any) { globals.Logger.Infof(f, a...) }

	target, err := strconv.Atoi(getenv("SMM_POOL_TARGET", "25"))
	if err != nil || target < 0 {
		globals.Logger.Warningf("SMM_POOL_TARGET must be a number >= 0; using 25")
		target = 25
	}

	p := &pool.Pool{
		DB: database.DB, Src: client, TargetPerBand: target,
		Import: levelimport.Options{CoursesDir: coursesDirPath},
		Logf:   func(f string, a ...any) { globals.Logger.Infof(f, a...) },
	}

	// * SMM_POOL_BANDS: where the pool's bands end, in percent (default 5,20,50 = 0-5%, 5-20%, 20-50%, 50-100%);
	// * SMM_DIFFICULTY_CEILING cuts the top band off below 100%
	if raw, ceil := getenv("SMM_POOL_BANDS", ""), difficultyCeiling(); raw != "" || ceil < 100 {
		bands := append([]pool.Band(nil), pool.DefaultBands...)

		if raw != "" {
			if parsed, err := pool.ParseBands(raw); err != nil {
				globals.Logger.Warningf("SMM_POOL_BANDS: %v; using the default 5,20,50", err)
			} else {
				bands = parsed
			}
		}

		if capped, err := pool.WithCeiling(bands, ceil); err != nil {
			globals.Logger.Warningf("SMM_DIFFICULTY_CEILING: %v; no ceiling is applied to the pool", err)
		} else {
			bands = capped
		}

		p.Bands = bands

		names := make([]string, len(bands))
		for i, b := range bands {
			names[i] = b.Name
		}

		globals.Logger.Infof("Level pool bands: %s, %d levels each", strings.Join(names, ", "), target)
	}

	database.OnMissing = p.OnMissing
	database.OnMissingProfile = func(pid uint32) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ok, err := p.EnsureProfile(ctx, int64(pid))
		if err != nil {
			globals.Logger.Warningf("Looking up player %d failed: %s", pid, err.Error())
		}

		return ok
	}

	globals.Logger.Infof("Fetching archived levels on demand from %s and %s (low request rate; SMM_FETCH=0 disables)", hostOf(api), hostOf(wayback))

	fetchPool = p

	// * archive profiles (the Mii and name the game shows next to a level) for the creators already in the catalog
	go p.BackfillProfiles(context.Background())

	// * optional size limit for the downloaded levels: the oldest go first (off by default)
	if mb := cacheLimitMB(); mb > 0 {
		globals.Logger.Infof("Level files are limited to %d MB: when they take more, the oldest levels are removed (SMM_CACHE_MAX_MB=0 turns this off)", mb)

		go p.KeepWithin(context.Background(), int64(mb)<<20, 5*time.Minute)
	}

	// * With a whole-archive index every level is a candidate and is downloaded when needed: no background stocking
	if x := openIndex(); x != nil {
		startIndexMode(p, x)
		return
	}

	growEvery, err := time.ParseDuration(getenv("SMM_POOL_GROW_EVERY", "10m"))
	if err != nil || growEvery < 0 {
		globals.Logger.Warningf("SMM_POOL_GROW_EVERY must be a duration such as 10m (or 0 to stop growth); using 10m")
		growEvery = 10 * time.Minute
	}

	growBy, err := strconv.Atoi(getenv("SMM_POOL_GROW_BY", "1"))
	if err != nil || growBy < 0 {
		globals.Logger.Warningf("SMM_POOL_GROW_BY must be a number >= 0; using 1")
		growBy = 1
	}

	growMax, err := strconv.Atoi(getenv("SMM_POOL_MAX", "250"))
	if err != nil || growMax < 0 {
		globals.Logger.Warningf("SMM_POOL_MAX must be a number >= 0; using 250")
		growMax = 250
	}

	if target == 0 && (growEvery == 0 || growBy == 0) {
		return
	}

	if growEvery > 0 && growBy > 0 {
		globals.Logger.Infof("Level pool: %d per band now, growing by %d per band every %s up to %d per band (SMM_POOL_GROW_EVERY=0 stops growth)", target, growBy, growEvery, growMax)
	}

	go p.Maintain(context.Background(), pool.Growth{Every: growEvery, By: growBy, Max: growMax})
}

var (
	ceilingOnce sync.Once
	ceilingPct  = 100
)

// difficultyCeiling reads SMM_DIFFICULTY_CEILING: the highest clear rate, in percent, a level may have to be stocked and
// offered in difficulty lists. 100 (the default) means no ceiling; a lower value filters out the levels that are too easy.
func difficultyCeiling() int {
	ceilingOnce.Do(func() {
		raw := strings.TrimSpace(getenv("SMM_DIFFICULTY_CEILING", ""))
		if raw == "" {
			return
		}

		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			globals.Logger.Warningf("SMM_DIFFICULTY_CEILING must be a whole number from 1 to 100; using 100 (no ceiling)")

			return
		}

		ceilingPct = n

		if n < 100 {
			globals.Logger.Infof("Difficulty ceiling: levels with a clear rate above %d%% are not stocked and not offered in difficulty lists (SMM_DIFFICULTY_CEILING)", n)
		}
	})

	return ceilingPct
}

func hostOf(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}

	if i := strings.IndexByte(raw, "/"[0]); i >= 0 {
		raw = raw[:i]
	}

	return raw
}

const settingsTemplate = `# Settings for the Super Mario Maker home server. Remove the leading # from a line to change it, then restart.
# Real environment variables and a .env file next to the program take precedence over this file.

# The address your Wii U / Cemu uses to reach this PC. It is detected automatically; set it if the wrong network is picked.
# Cemu only accepts an IPv4 address here (at most 15 characters).
#SMM_PUBLIC_HOST=192.168.1.50

# Fetch levels from the public archive (api.bobac-analytics.com and web.archive.org) while you play. 0 = fully offline.
#SMM_FETCH=1
# Levels to keep ready in each of 4 clear-rate bands (the first download takes roughly 5 minutes per 100 levels).
#SMM_POOL_TARGET=25
# Slow growth afterwards: add this many levels per band every so often, up to a maximum per band. SMM_POOL_GROW_EVERY=0 stops it.
#SMM_POOL_GROW_EVERY=10m
#SMM_POOL_GROW_BY=1
#SMM_POOL_MAX=250
# Where the pool's difficulty bands end, in percent clear rate. The pool keeps the same number of levels in every band, so
# this decides how much stock each difficulty gets. 5,20,50 means the bands 0-5%, 5-20%, 20-50% and 50-100% (the default);
# for example 10,30,60,85 makes five bands. Rising whole numbers from 1 to 99, at most 8. Does not change cache builds.
#SMM_POOL_BANDS=5,20,50
# Highest clear rate (percent) a level may have to be kept and offered in difficulty lists. 100 = no ceiling. Set it lower, for
# example 80, to filter out levels that are too easy: the pool stops downloading them and the difficulty lists skip any it holds.
#SMM_DIFFICULTY_CEILING=100
# The 100 Mario Challenge asks for "Normal" as the failure rates 0-74, which includes Easy (0-34). By default the server narrows
# that to 35-74 so Normal does not overlap Easy. 0 gives the game's range as it asks for it (a mix of Easy and Normal levels).
#SMM_NARROW_NORMAL=1
# Levels remembered per player (a rolling window, so the file stays small): random lists offer other levels first. "off" turns it off.
#SMM_HISTORY_LEVELS=1000
# Wii U with Inkay: by default only the game is answered and the console's own programs (Wii U Menu, friends, Miiverse) are left
# alone. 1 sends them on to the real Pretendo Network instead (needs a Pretendo account on the console).
#SMM_PROXY_PRETENDO=1
# Size limit for the downloaded levels, in megabytes. When they take more, the oldest levels are removed (levels you starred or
# made are kept; the game can fetch a removed level again). 0 = no limit (the default). Only used while fetching is on.
#SMM_CACHE_MAX_MB=0
# How levels are sorted by difficulty. A level with few attempts has an unreliable clear rate (3 clears in 3 attempts is not
# "100%"), so its score is pulled towards a typical level: MEAN,STRENGTH = that level's clear rate (0..1) and how many attempts'
# worth of evidence it carries. The default 0.2,3 leaves levels with 40+ attempts within about a point of their raw rate.
# "off" uses the raw clear rate. A locked cache keeps the prior it was built with; rebuild an index after changing this.
#SMM_DIFFICULTY_PRIOR=0.2,3

# Whole-archive index: lets every archived level come up in random lists (build one with: smm-server index build --from <levels.jsonl> --out data/index.sqlite).
# Used automatically when data/index.sqlite exists. 0 = ignore it.
#SMM_INDEX=data/index.sqlite
# Seconds a course list may wait for its levels to finish downloading before it is sent (0 = do not wait).
#SMM_LIST_WAIT=0
# Log every request the game makes (and every profile lookup). Only for finding problems: it fills the log quickly.
#SMM_TRACE=0
# Challenge seeds (same seed = same "random" levels for everyone) are set on http://<this PC>:8080/seed

# Ports. If the console cannot connect, allow these in your firewall: TCP 8080, 8888 and 9000, UDP 60000 and 60001.
#SMM_ACCOUNT_PORT=8080
#SMM_S3_PORT=9000
# Port of the proxy a Wii U patched with Inkay is set to use (docs/setup-wiiu.md). 0 = off.
#SMM_PROXY_PORT=8888
#PN_SMM_AUTHENTICATION_SERVER_PORT=60000
#PN_SMM_SECURE_SERVER_PORT=60001
`

// loadSettingsFile reads <data>/settings.env (creating a commented template on the first run). Settings that are
// already present in the environment win.
func loadSettingsFile(dataDir string) {
	path := filepath.Join(dataDir, "settings.env")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(settingsTemplate), 0o644); err != nil {
			globals.Logger.Warningf("Cannot write %s: %s", path, err.Error())
		}

		return
	}

	if err := godotenv.Load(path); err != nil {
		globals.Logger.Warningf("Cannot read %s: %s", path, err.Error())
	}
}

// preflightUDP fails early, with advice, if a NEX port is taken (the server library would otherwise just log an error).
func preflightUDP() {
	for _, k := range []string{"PN_SMM_AUTHENTICATION_SERVER_PORT", "PN_SMM_SECURE_SERVER_PORT"} {
		port := os.Getenv(k)

		pc, err := net.ListenPacket("udp", ":"+port)
		if err != nil {
			globals.Logger.Criticalf("UDP port %s is not available (%s). Another program is probably using it; change it with %s in %s",
				port, err.Error(), k, filepath.Join(dataDirPath, "settings.env"))
			os.Exit(1)
		}

		pc.Close()
	}
}

// installShutdownHandler makes Ctrl+C (or closing the window) save the catalog properly: closing the database merges
// its write-ahead log, so the data folder can be copied safely afterwards.
func installShutdownHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ch
		globals.Logger.Info("Shutting down: saving the catalog...")

		if err := database.Close(); err != nil {
			globals.Logger.Warningf("Closing the catalog: %s", err.Error())
		}

		os.Exit(0)
	}()
}

func wantsHelp() bool {
	if len(os.Args) < 2 {
		return false
	}

	switch os.Args[1] {
	case "-h", "--help", "-help", "help", "/?":
		return true
	}

	return false
}

func printHelp() {
	fmt.Print(`Super Mario Maker home server

Just run it. Nothing needs to be configured: it creates a "data" folder next to where you start it, picks this PC's
network address, generates its own keys and starts fetching archived levels.

  data/network_services.xml  copy this next to Cemu's settings.xml, then in Cemu choose Network Service: Custom
  data/settings.env          optional settings (address, ports, how many levels to keep); commented, edit and restart
  data/courses, catalog.sqlite   the level archive. Copy the folder only while the server is stopped.

Stop it with Ctrl+C (this saves the catalog).

Challenge seeds (everyone gets the same "random" levels): open http://<this PC>:8080/seed
Account helper:   smm-server account <name | path/to/account.dat>   (the game's player ID must match what this server assigns)
Level cache:      smm-server cache build --levels N [--seed WORDS] (same seed = same levels everywhere; see docs/cache.md)
Course IDs:       smm-server code <9007-0000-00BB-FD3B | data ID>   (the ID the game shows <-> the number in the server's log)
Event courses:    smm-server events build events.json (make the game's event menu list; see docs/event-courses.md)
Whole-archive index: smm-server index build --from <levels.jsonl[.gz|.zst]> --out data/index.sqlite, then restart (see README)

Environment variables override settings.env; SMM_DATA_DIR chooses a different data folder.
`)
}

// printBanner summarises how to connect, once the servers are up.
func printBanner() {
	var levels int

	if database.DB != nil {
		database.DB.QueryRow(`SELECT COUNT(*) FROM datastore.level_sources`).Scan(&levels)
	}

	abs, err := filepath.Abs(dataDirPath)
	if err != nil {
		abs = dataDirPath
	}

	var b strings.Builder

	b.WriteString("\n==================================================================================\n")
	b.WriteString(" Super Mario Maker home server is running\n\n")
	fmt.Fprintf(&b, "   THIS PC'S ADDRESS (type this on your console / Cemu):  %s\n", publicHost)

	if others := otherLANIPv4(publicHost); len(others) > 0 {
		fmt.Fprintf(&b, "   This PC also has: %s\n", strings.Join(others, ", "))
		b.WriteString("   If the console cannot reach the address above, use one of these instead: put\n")
		fmt.Fprintf(&b, "   SMM_PUBLIC_HOST=<address> in %s and restart.\n", filepath.Join(abs, "settings.env"))
	}

	if publicHost == "127.0.0.1" {
		b.WriteString("   !! No network address was found. Connect this PC to your home network, then restart.\n")
	}

	fmt.Fprintf(&b, "\n   Levels in the archive so far:  %d  (more are fetched in the background)\n", levels)
	fmt.Fprintf(&b, "   Data folder:                   %s\n\n", abs)

	fmt.Fprintf(&b, "   Cemu:  copy %s next to Cemu's settings.xml\n", filepath.Join(abs, "network_services.xml"))
	b.WriteString("          then Options > General settings > Account > Network Service: Custom\n")

	if proxyPort != "" {
		b.WriteString("\n   Wii U (with Inkay): System Settings > Internet > Connect to the Internet > Connection List >\n")
		b.WriteString("          your connection > Change Settings > Proxy Settings > Set, then enter:\n")
		fmt.Fprintf(&b, "              proxy server:  %s\n", publicHost)
		fmt.Fprintf(&b, "              port:          %s        (choose \"Don't Use Authentication\")\n", proxyPort)
	}

	fmt.Fprintf(&b, "\n   If the console cannot connect, allow in the firewall: TCP %s, %s", getenv("SMM_ACCOUNT_PORT", "8080"), getenv("SMM_S3_PORT", "9000"))

	if proxyPort != "" {
		fmt.Fprintf(&b, ", %s", proxyPort)
	}

	fmt.Fprintf(&b, "  and  UDP %s, %s\n", os.Getenv("PN_SMM_AUTHENTICATION_SERVER_PORT"), os.Getenv("PN_SMM_SECURE_SERVER_PORT"))
	fmt.Fprintf(&b, "   Settings: %s     Stop: Ctrl+C\n", filepath.Join(abs, "settings.env"))
	b.WriteString("==================================================================================\n\n")

	fmt.Print(b.String())
}

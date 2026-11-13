package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

const cacheUsage = `Usage:
  smm-server cache build --levels N [--seed WORDS] [--force]   download levels into the data folder
  smm-server cache info                                          what the data folder holds
  smm-server cache export FILE.zip                               pack the cache so others can unpack it
  smm-server cache import FILE.zip [--force]                     unpack a cache into an empty data folder
  smm-server cache lock | unlock                                 keep (or stop keeping) the set of levels fixed

A cache is a data folder filled on purpose. With --seed the seed alone decides which levels are downloaded, so everyone who
builds the same seed to the same size gets the same levels, and the server can give everybody the same lists. Without --seed
the levels are random. --levels is rounded up to a multiple of 4 (the four difficulty bands are filled evenly) and each level
takes about 50 KB. Building takes roughly two seconds per level, at the polite request rate of the public archive services.
Building again with a larger --levels (same seed) only adds levels; an interrupted build carries on where it stopped.
Close the server before building, importing or locking. SMM_DATA_DIR chooses the data folder (default ./data).
`

// runCacheCommand implements "smm-server cache ...". It returns the process exit code.
func runCacheCommand(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(cacheUsage)

		return 0
	}

	dir := os.Getenv("SMM_DATA_DIR")
	if dir == "" {
		dir = "./data"
	}

	switch args[0] {
	case "build":
		return cacheBuild(dir, args[1:])
	case "info":
		return cacheInfo(dir)
	case "export":
		return cacheExport(dir, args[1:])
	case "import":
		return cacheImport(dir, args[1:])
	case "lock", "unlock":
		return cacheLock(dir, args[0] == "lock")
	}

	fmt.Fprintf(os.Stderr, "unknown cache command %q\n\n%s", args[0], cacheUsage)

	return 2
}

func cacheBuild(dir string, args []string) int {
	fs := flag.NewFlagSet("cache build", flag.ContinueOnError)
	seed := fs.String("seed", "", "words that decide which levels are downloaded (empty: random levels)")
	levels := fs.Int("levels", 0, "how many levels")
	force := fs.Bool("force", false, "allow a seeded build in a folder that already holds levels")

	if err := fs.Parse(args); err != nil || *levels <= 0 {
		fmt.Fprint(os.Stderr, "cache build needs --levels N\n\n", cacheUsage)

		return 2
	}

	db, err := openCatalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer db.Close()

	courses := filepath.Join(dir, "courses")
	if err := os.MkdirAll(courses, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	client := upstream.New(getenv("SMM_FETCH_API", upstream.DefaultAPI), getenv("SMM_FETCH_WAYBACK", upstream.DefaultWayback), 0, 0)
	client.Logf = func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }

	p := &pool.Pool{
		DB: db, Src: client, Import: levelimport.Options{CoursesDir: courses},
		Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}

	rounded := (*levels + 3) / 4 * 4

	fmt.Printf("Building a cache of %d levels in %s (about %d MB, roughly %d minutes)", rounded, dir, rounded*50/1024+1, rounded*2/60+1)

	if *seed != "" {
		fmt.Printf(" from the seed %q", strings.Join(strings.Fields(*seed), " "))
	}

	fmt.Println(".\nCtrl+C stops it; running the same command again carries on.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	started := time.Now()

	m, err := p.BuildCache(ctx, pool.BuildOptions{Seed: *seed, Levels: *levels, Dir: dir, Force: *force})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())

		if m != nil {
			fmt.Fprintf(os.Stderr, "Saved so far: %d levels, fingerprint %s.\n", m.Levels, m.Fingerprint)
		}

		return 1
	}

	fmt.Printf("\nDone in %s: %d levels, fingerprint %s.\n", time.Since(started).Round(time.Second), m.Levels, m.Fingerprint)

	if m.Seed != "" {
		fmt.Printf("Everyone who builds the seed %q with --levels %d gets this fingerprint. Start the server to play: it keeps the set fixed and gives every player the same lists.\n", m.Seed, m.Wanted)
	}

	return 0
}

func cacheInfo(dir string) int {
	m, err := pool.ReadManifest(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	db, err := openCatalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer db.Close()

	fp, n, err := pool.CacheFingerprint(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	size, _ := (&pool.Pool{Import: levelimport.Options{CoursesDir: filepath.Join(dir, "courses")}}).CourseFilesSize()

	fmt.Printf("data folder:  %s\nlevels:       %d (%.1f MB of level files)\nfingerprint:  %s\n", dir, n, float64(size)/(1<<20), fp)

	if m == nil {
		fmt.Println("cache:        none (this folder was not built with `smm-server cache build`)")

		return 0
	}

	kind := "random levels (not repeatable)"
	if m.Seed != "" {
		kind = fmt.Sprintf("seed %q (repeatable)", m.Seed)
	}

	fmt.Printf("cache:        %s\nlocked:       %v\nlevels per band: %v of %d wanted\nbuilt:        %s\n", kind, m.Locked, m.PerBand, m.Wanted, m.Built.Local().Format("2006-01-02 15:04"))

	if m.Fingerprint != fp {
		fmt.Printf("note: the manifest says fingerprint %s; levels were added or removed since the last build or lock.\n", m.Fingerprint)
	}

	return 0
}

func cacheExport(dir string, args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "cache export needs a file name, for example: smm-server cache export my-cache.zip\n")

		return 2
	}

	db, err := openCatalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer db.Close()

	m, err := pool.ExportCache(db, dir, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	fmt.Printf("wrote %s: %d levels, fingerprint %s. Accounts, profiles of players, records and seeds of this server are not in it.\n", args[0], m.Levels, m.Fingerprint)

	return 0
}

func cacheImport(dir string, args []string) int {
	fs := flag.NewFlagSet("cache import", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace the catalog and level files of the data folder")

	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, "cache import needs a file name, for example: smm-server cache import my-cache.zip\n")

		return 2
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	m, err := pool.ImportCache(fs.Arg(0), dir, *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	fmt.Printf("unpacked into %s: %d levels, fingerprint %s. Start the server to play.\n", dir, m.Levels, m.Fingerprint)

	return 0
}

func cacheLock(dir string, lock bool) int {
	m, err := pool.ReadManifest(dir)
	if err != nil || m == nil {
		fmt.Fprintln(os.Stderr, "this data folder is not a cache (there is no cache.json); build one with: smm-server cache build")

		return 1
	}

	db, err := openCatalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer db.Close()

	fp, n, err := pool.CacheFingerprint(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	m.Locked, m.Fingerprint, m.Levels = lock, fp, n

	if err := m.Write(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	if lock {
		fmt.Printf("locked: the server will not add levels (fingerprint %s, %d levels)\n", fp, n)
	} else {
		fmt.Println("unlocked: the server may fetch levels again, so this folder will no longer match other copies of the cache")
	}

	return 0
}

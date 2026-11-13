package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/index"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
	"github.com/klauspost/compress/zstd"
)

const indexUsage = `Usage:
  smm-server index build --from <levels.jsonl[.gz|.zst]> --out <index.sqlite>
  smm-server index info <index.sqlite>

"build" turns a metadata dump (one JSON level record per line, shaped like the metadata API's answer: url, levelid,
name, creatorid, clears, total_attempts, stars, uploadTime) into a compact index of every level. Put the result at
data/index.sqlite (or point SMM_INDEX at it) and the server draws random levels from the whole archive.
`

// runIndexCommand implements "smm-server index ...". It returns the process exit code.
func runIndexCommand(args []string) int {
	if len(args) == 0 {
		fmt.Print(indexUsage)
		return 2
	}

	switch args[0] {
	case "build":
		fs := flag.NewFlagSet("index build", flag.ContinueOnError)
		from := fs.String("from", "", "metadata dump (JSON Lines; .gz and .zst are decompressed)")
		out := fs.String("out", "", "index file to write")
		limit := fs.Int("limit", 0, "stop after this many records (0 = all; for trying a dump out)")

		if err := fs.Parse(args[1:]); err != nil || *from == "" || *out == "" {
			fmt.Print(indexUsage)
			return 2
		}

		if err := buildIndex(*from, *out, *limit); err != nil {
			fmt.Fprintln(os.Stderr, "index build failed:", err)
			return 1
		}

		return 0
	case "info":
		if len(args) != 2 {
			fmt.Print(indexUsage)
			return 2
		}

		x, err := index.Open(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer x.Close()

		printIndexSummary(x)

		return 0
	}

	fmt.Print(indexUsage)

	return 2
}

func openDump(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	switch {
	case strings.HasSuffix(path, ".zst"):
		d, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}

		return struct {
			io.Reader
			io.Closer
		}{d, closerFunc(func() error { d.Close(); return f.Close() })}, nil
	case strings.HasSuffix(path, ".gz"):
		g, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}

		return struct {
			io.Reader
			io.Closer
		}{g, closerFunc(func() error { g.Close(); return f.Close() })}, nil
	}

	return f, nil
}

type closerFunc func() error

func (c closerFunc) Close() error { return c() }

func buildIndex(from, out string, limit int) error {
	in, err := openDump(from)
	if err != nil {
		return err
	}
	defer in.Close()

	b, err := index.Create(out)
	if err != nil {
		return err
	}

	ok := false

	defer func() {
		if !ok {
			b.Abort()
		}
	}()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)

	var lines, parsed, unreadable, skipped int

	reasons := map[string]int{}
	started := time.Now()

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		lines++

		if limit > 0 && lines > limit {
			break
		}

		lvl, err := upstream.ParseRecord(line)
		if err != nil {
			unreadable++

			// * a file whose first records are all unusable is the wrong kind of file: say what it contains, then stop
			if parsed == 0 && unreadable >= 20 {
				return fmt.Errorf("the first %d records are not level records (%v). The first record has these fields: %s", unreadable, err, recordKeys(line))
			}

			continue
		}

		parsed++

		err = b.Add(index.Record{
			ID: lvl.LevelID, Name: lvl.Name, Creator: lvl.CreatorID, Clears: lvl.Clears, Attempts: lvl.TotalAttempts,
			Stars: lvl.Stars, Upload: lvl.UploadTime, URL: lvl.URL,
		})

		var se *index.SkipError

		switch {
		case errors.As(err, &se):
			skipped++
			reasons[se.Reason]++
		case err != nil:
			return err
		}

		if lines%500000 == 0 {
			fmt.Fprintf(os.Stderr, "  %d records read (%d usable) in %s\n", lines, parsed-skipped, time.Since(started).Round(time.Second))
		}
	}

	if err := sc.Err(); err != nil {
		return err
	}

	st, err := b.Finish()
	if err != nil {
		return err
	}

	ok = true

	fmt.Printf("read %d records: %d indexed, %d unreadable, %d skipped\n", lines, st.Total, unreadable, skipped)

	keys := make([]string, 0, len(reasons))
	for r := range reasons {
		keys = append(keys, r)
	}

	sort.Strings(keys)

	for _, r := range keys {
		fmt.Printf("  skipped %d: %s\n", reasons[r], r)
	}

	x, err := index.Open(out)
	if err != nil {
		return err
	}
	defer x.Close()

	printIndexSummary(x)

	return nil
}

func recordKeys(line []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return "(the line is not a JSON object)"
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return strings.Join(keys, ", ")
}

func printIndexSummary(x *index.Index) {
	counts := x.Counts()

	fmt.Printf("levels: %d   fingerprint: %s   (players need the same fingerprint to get the same levels from a seed)\n", x.Total(), x.Fingerprint())

	for _, band := range [][2]int{{0, 4}, {5, 19}, {20, 49}, {50, 100}} {
		var n int64
		for b := band[0]; b <= band[1]; b++ {
			n += counts[b]
		}

		fmt.Printf("  clear rate %3d-%3d%%: %9d levels (%4.1f%%)\n", band[0], band[1], n, 100*float64(n)/float64(x.Total()))
	}
}

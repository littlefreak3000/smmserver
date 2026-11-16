package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PretendoNetwork/super-mario-maker/events"
)

const eventsUsage = `Usage:
  smm-server events dump FILE                   show what an event course list (data ID 900000) contains
  smm-server events example                     print an example events.json
  smm-server events build events.json [--out FILE]
                                                write an event course list; default FILE is <data folder>/courses/900000.bin

The game builds its "event courses" menu from the file served as data ID 900000 (see docs/event-courses.md). "build" makes
one from a JSON file: each event names the data ID of the course it opens, a title and a description (for every language, or
per language), a start date, an optional end date and an optional banner pack. Close the server first and restart it
afterwards. The courses the events open must exist in the catalog. SMM_DATA_DIR chooses the data folder (default ./data).
`

// runEventsCommand implements "smm-server events ...". It returns the process exit code.
func runEventsCommand(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(eventsUsage)

		return 0
	}

	switch args[0] {
	case "dump":
		if len(args) != 2 {
			fmt.Fprint(os.Stderr, "events dump needs a file name\n\n", eventsUsage)

			return 2
		}

		return eventsDump(args[1])
	case "example":
		fmt.Print(events.Example)

		return 0
	case "build":
		return eventsBuild(args[1:])
	}

	fmt.Fprintf(os.Stderr, "unknown events command %q\n\n%s", args[0], eventsUsage)

	return 2
}

func eventsDump(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	list, err := events.Parse(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	fmt.Printf("%d events\n", len(list))

	for _, e := range list {
		fmt.Printf("%d  %s .. %s  pack %-14q  %s / %s\n", e.ID, e.Start.Format("2006-01-02 15:04"), e.End.Format("2006-01-02 15:04"), e.Pack, e.Title[1], e.Title[0])
	}

	return 0
}

func eventsBuild(args []string) int {
	fs := flag.NewFlagSet("events build", flag.ContinueOnError)
	out := fs.String("out", "", "file to write (default: <data folder>/courses/900000.bin)")

	// * the file name may come before or after --out
	var file string

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		file, args = args[0], args[1:]
	}

	if err := fs.Parse(args); err != nil || (file == "") == (fs.NArg() == 0) || fs.NArg() > 1 {
		fmt.Fprint(os.Stderr, "events build needs a JSON file, for example: smm-server events build events.json\n")

		return 2
	}

	if file == "" {
		file = fs.Arg(0)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	list, err := events.FromJSON(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	b, err := events.Marshal(list)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	if *out == "" {
		dir := os.Getenv("SMM_DATA_DIR")
		if dir == "" {
			dir = "./data"
		}

		*out = filepath.Join(dir, "courses", "900000.bin")
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	fmt.Printf("wrote %s: %d events, %d bytes. Restart the server so the game fetches it.\n", *out, len(list), len(b))

	return 0
}

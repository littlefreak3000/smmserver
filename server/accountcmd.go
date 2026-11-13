package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/homeaccount"
)

const accountUsage = `Usage:
  smm-server account <name>                 print the player ID this server gives that account name
  smm-server account <path/account.dat>     set PrincipalId in a Cemu (or dumped) account file to that ID
  smm-server account new <name> <mlc01>     create a new Cemu account called <name> in Cemu's mlc01 folder
  smm-server account adopt <account.dat>    make the server use the player ID that is already in an account file
                                            (for a Wii U whose account must keep its own ID)

The game logs in with the player ID stored in the account file, and this server decides which ID belongs to which name.
The two must agree, or the login is refused. An account file is Cemu's mlc01/usr/save/system/act/<id>/account.dat; the
original is kept as account.dat.bak. Close Cemu first. SMM_DATA_DIR chooses the data folder (default ./data).
`

// runAccountCommand implements "smm-server account ...". It returns the process exit code.
func runAccountCommand(args []string) int {
	if len(args) == 3 && args[0] == "new" {
		return createAccount(args[1], args[2])
	}

	if len(args) == 2 && args[0] == "adopt" {
		return adoptAccount(args[1])
	}

	if len(args) != 1 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(accountUsage)

		if len(args) == 1 {
			return 0
		}

		return 2
	}

	target := args[0]
	name := target

	var lines []string

	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		raw, err := os.ReadFile(target)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read the account file:", err)

			return 1
		}

		lines = splitLines(string(raw))
		name = ""

		for _, l := range lines {
			if v, ok := strings.CutPrefix(l, "AccountId="); ok {
				name = strings.TrimSpace(v)
			}
		}

		if name == "" {
			fmt.Fprintln(os.Stderr, "this account has no network account name (AccountId is empty). Use an account that has one, such as the one dumped from your Wii U, or create one with: smm-server account new <name> <mlc01>")

			return 1
		}
	}

	pid, err := lookupPID(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot get a player ID:", err)

		return 1
	}

	fmt.Printf("account name %q -> player ID %d (PrincipalId=%x)\n", name, pid, pid)

	if lines == nil {
		return 0
	}

	found := false

	for i, l := range lines {
		if strings.HasPrefix(l, "PrincipalId=") {
			lines[i] = fmt.Sprintf("PrincipalId=%x", pid)
			found = true
		}
	}

	if !found {
		fmt.Fprintln(os.Stderr, "the file has no PrincipalId line; it does not look like an account file")

		return 1
	}

	if _, err := os.Stat(target + ".bak"); os.IsNotExist(err) {
		if raw, err := os.ReadFile(target); err == nil {
			_ = os.WriteFile(target+".bak", raw, 0o644)
		}
	}

	if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write the account file:", err)

		return 1
	}

	fmt.Println("updated", target)

	return 0
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// createAccount makes a new account in Cemu's mlc01 folder from the account Cemu created itself (80000001), with a network
// account name and the player ID this server assigns to it.
func createAccount(name, mlc string) int {
	if name == "" || len(name) > 16 || strings.ContainsAny(name, " \t=/\\") {
		fmt.Fprintln(os.Stderr, "the account name must be 1 to 16 letters, digits, - or _")

		return 1
	}

	act := filepath.Join(mlc, "usr", "save", "system", "act")

	raw, err := os.ReadFile(filepath.Join(act, "80000001", "account.dat"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot find Cemu's own account in", act, "- start Cemu once with this mlc01 folder first, then try again")

		return 1
	}

	id := uint32(0x80000002)
	for {
		if _, err := os.Stat(filepath.Join(act, fmt.Sprintf("%x", id))); os.IsNotExist(err) {
			break
		}

		id++
	}

	pid, err := lookupPID(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot get a player ID:", err)

		return 1
	}

	random := func(n int) string {
		b := make([]byte, n)
		_, _ = rand.Read(b)

		return hex.EncodeToString(b)
	}

	set := map[string]string{
		"PersistentId":           fmt.Sprintf("%x", id),
		"TransferableIdBase":     "2" + random(7),
		"Uuid":                   random(16),
		"AccountId":              name,
		"BirthYear":              "7c3",
		"BirthMonth":             "6",
		"BirthDay":               "12",
		"Gender":                 "1",
		"Country":                "31",
		"PrincipalId":            fmt.Sprintf("%x", pid),
		"IsPasswordCacheEnabled": "1",
		"AccountPasswordCache":   random(32),
	}

	lines := splitLines(string(raw))

	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok {
			if v, found := set[k]; found {
				lines[i] = k + "=" + v
			}
		}
	}

	dir := filepath.Join(act, fmt.Sprintf("%x", id))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create", dir+":", err)

		return 1
	}

	if err := os.WriteFile(filepath.Join(dir, "account.dat"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write the account:", err)

		return 1
	}

	fmt.Printf("created account %q (Cemu account %x, player ID %d)\n", name, id, pid)
	fmt.Println("In Cemu: Options > General settings > Account, choose it and set Network Service to Custom.")

	return 0
}

// openCatalog opens the catalog in the data folder, creating it if needed.
func openCatalog() (*sql.DB, error) {
	dataDir := os.Getenv("SMM_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}

	db, err := database.Open(filepath.Join(dataDir, "catalog.sqlite"))
	if err != nil {
		return nil, fmt.Errorf("cannot open the catalog in %s: %w", dataDir, err)
	}

	return db, nil
}

// lookupPID asks the catalog which player ID an account name has, registering it on first sight.
func lookupPID(name string) (uint32, error) {
	db, err := openCatalog()
	if err != nil {
		return 0, err
	}

	defer db.Close()

	return homeaccount.New(homeaccount.Config{DB: db}).PIDFor(name)
}

// adoptAccount registers the name and player ID that an existing account file holds, so the server hands that ID out
// instead of deriving one. A console keeps the ID of its network account in its own files and cannot be told another one.
func adoptAccount(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read the account file:", err)

		return 1
	}

	var name, principal string

	for _, l := range splitLines(string(raw)) {
		if v, ok := strings.CutPrefix(l, "AccountId="); ok {
			name = strings.TrimSpace(v)
		}

		if v, ok := strings.CutPrefix(l, "PrincipalId="); ok {
			principal = strings.TrimSpace(v)
		}
	}

	pid, err := strconv.ParseUint(principal, 16, 32)
	if name == "" || err != nil || pid == 0 {
		fmt.Fprintln(os.Stderr, "this account has no network account (AccountId or PrincipalId is empty). Use the account of a Wii U user that has a Nintendo Network ID or Pretendo ID.")

		return 1
	}

	db, err := openCatalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer db.Close()

	var other string

	if db.QueryRow(`SELECT nnid FROM datastore.accounts WHERE pid = $1 AND nnid != $2`, pid, name).Scan(&other) == nil {
		fmt.Fprintf(os.Stderr, "player ID %d already belongs to the account %q on this server; remove that account first\n", pid, other)

		return 1
	}

	if _, err := db.Exec(`DELETE FROM datastore.accounts WHERE nnid = $1`, name); err != nil {
		fmt.Fprintln(os.Stderr, "cannot update the catalog:", err)

		return 1
	}

	if _, err := db.Exec(`INSERT INTO datastore.accounts (nnid, pid, created) VALUES ($1, $2, $3)`, name, pid, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, "cannot update the catalog:", err)

		return 1
	}

	fmt.Printf("the server now gives the account %q the player ID %d (PrincipalId=%x)\n", name, pid, pid)

	return 0
}

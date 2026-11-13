// Package challenge holds the seeds that make "random" levels repeatable.
//
// The game has no field to type a seed into, so a seed is set on the server: for everyone, or for one player's NNID.
// While a seed is active, a list request carries no chance at all: the levels depend only on the seed and on which list
// was asked for, so two players with the same seed get the same levels (see index.NewSeeded for the exact contract).
// Players on different servers get the same levels only if their archives are the same: compare the challenge code
// (seed + the index fingerprint) shown on the seed page.
package challenge

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/PretendoNetwork/super-mario-maker/index"
)

// Everyone is the scope of a seed that applies to every player without a seed of their own.
const Everyone = "*"

const maxSeedLen = 64

// Store keeps seeds in the catalog database (table challenge_seeds).
type Store struct {
	DB *sql.DB
}

// Entry is one seed and who it applies to.
type Entry struct {
	Scope   string // Everyone or "nnid:<name>"
	Seed    string
	Updated time.Time
}

// PlayerScope is the scope of a seed for one NNID.
func PlayerScope(nnid string) string { return "nnid:" + strings.TrimSpace(nnid) }

func validNNID(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}

	for _, c := range n {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}

	return true
}

func validScope(scope string) error {
	if scope == Everyone {
		return nil
	}

	if n, ok := strings.CutPrefix(scope, "nnid:"); ok && validNNID(n) {
		return nil
	}

	return fmt.Errorf("challenge: %q is not a valid scope (use * or nnid:<name>)", scope)
}

// CleanSeed trims a typed seed and checks it. The stored text keeps the player's spelling; matching ignores case and spacing.
func CleanSeed(seed string) (string, error) {
	seed = strings.Join(strings.Fields(seed), " ")
	if seed == "" {
		return "", errors.New("challenge: a seed cannot be empty")
	}

	if len([]rune(seed)) > maxSeedLen {
		return "", fmt.Errorf("challenge: a seed can be at most %d characters", maxSeedLen)
	}

	for _, c := range seed {
		if unicode.IsControl(c) {
			return "", errors.New("challenge: a seed cannot contain control characters")
		}
	}

	return seed, nil
}

// Set activates a seed for a scope, replacing any earlier one.
func (s *Store) Set(scope, seed string) error {
	if err := validScope(scope); err != nil {
		return err
	}

	seed, err := CleanSeed(seed)
	if err != nil {
		return err
	}

	_, err = s.DB.Exec(`INSERT INTO datastore.challenge_seeds (scope, seed, updated) VALUES ($1, $2, $3)
		ON CONFLICT (scope) DO UPDATE SET seed = excluded.seed, updated = excluded.updated`, scope, seed, time.Now().UTC())

	return err
}

// Clear removes the seed of a scope (levels become random again for it).
func (s *Store) Clear(scope string) error {
	if err := validScope(scope); err != nil {
		return err
	}

	_, err := s.DB.Exec(`DELETE FROM datastore.challenge_seeds WHERE scope = $1`, scope)

	return err
}

// Effective returns the seed that applies to a player: their own if they have one, otherwise the one for everyone.
// It returns "" when no seed is active. Problems reading the database count as "no seed": a list must still be served.
func (s *Store) Effective(nnid string) string {
	for _, scope := range []string{PlayerScope(nnid), Everyone} {
		if scope == "nnid:" {
			continue
		}

		var seed string
		if err := s.DB.QueryRow(`SELECT seed FROM datastore.challenge_seeds WHERE scope = $1`, scope).Scan(&seed); err == nil {
			return seed
		}
	}

	return ""
}

// List returns every active seed, the one for everyone first.
func (s *Store) List() ([]Entry, error) {
	rows, err := s.DB.Query(`SELECT scope, seed, updated FROM datastore.challenge_seeds ORDER BY scope = '*' DESC, scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry

	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Scope, &e.Seed, &e.Updated); err != nil {
			return nil, err
		}

		out = append(out, e)
	}

	return out, rows.Err()
}

// RandomSeed makes a short, easy to read-out seed (no look-alike characters).
func RandomSeed() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"

	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}

	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}

	return string(b)
}

// Code is what players compare to be sure they will get the same levels: the (normalized) seed and the index fingerprint.
func Code(seed, fingerprint string) string {
	return index.NormalizeSeed(seed) + "-" + fingerprint
}

package index

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// AlgorithmVersion is mixed into every seeded stream. Seeds are a contract between players: the same seed and the
// same index must give the same levels on every machine and every build. Anything that changes the stream, the way
// bucket and position are drawn, or how a list is cut MUST bump this number (and the known-answer test).
const AlgorithmVersion = 1

// Source is the random source used to pick levels. A request that carries a seed gets a deterministic Source;
// everything else gets a random one.
type Source interface {
	// IntN returns a uniform integer in [0, n). n must be positive.
	IntN(n int) int
}

// stream is SplitMix64 with unbiased range reduction. It is written out here, not borrowed from a library, so that
// no library or Go version can change what a seed means.
type stream struct{ state uint64 }

func (s *stream) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb

	return z ^ (z >> 31)
}

func (s *stream) IntN(n int) int {
	if n <= 0 {
		panic("index: IntN needs a positive n")
	}

	un := uint64(n)
	threshold := -un % un // reject the few values that would make some results more likely than others

	for {
		if v := s.next(); v >= threshold {
			return int(v % un)
		}
	}
}

// NewRandom returns a Source seeded from the operating system's randomness.
func NewRandom() Source {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}

	return &stream{state: binary.LittleEndian.Uint64(b[:])}
}

// NormalizeSeed makes seeds forgiving to type: case and surrounding or repeated spaces do not matter, so "Mario  Party"
// and "mario party" are the same seed.
func NormalizeSeed(seed string) string {
	return strings.ToLower(strings.Join(strings.Fields(seed), " "))
}

// NewSeeded returns a deterministic Source for a seed and a context (which list is being drawn, e.g. "range|300000000|1|49").
// The same seed and context always give the same stream; different contexts give independent streams.
func NewSeeded(seed, context string) Source {
	sum := sha256.Sum256([]byte(fmt.Sprintf("smm-seed-v%d\x00%s\x00%s", AlgorithmVersion, NormalizeSeed(seed), context)))

	return &stream{state: binary.LittleEndian.Uint64(sum[:8])}
}

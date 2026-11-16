// Package difficulty turns a level's counts of clears and attempts into the difficulty score that decides which lists it
// can appear in.
//
// A raw clear rate (clears / attempts) is a poor guide when a level has few attempts: 3 clears in 3 attempts is "100 %", 0 in 4
// is "0 %". The score here is the posterior mean of the true clear rate under a Beta prior, which behaves like extra attempts
// at a typical level's clear rate:
//
//	score = (clears + mean × strength) / (attempts + strength)
//
// With many attempts the prior does not matter (the score is the raw rate); with few, the score moves towards the typical
// level. See docs/difficulty.md for the data the defaults come from.
package difficulty

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Prior is the "typical level" a score is pulled towards: Mean is its clear rate (0..1), Strength how many attempts' worth of
// evidence it carries. The zero value does nothing, so the score is the raw clear rate.
type Prior struct {
	Mean     float64
	Strength float64
}

// Default is fitted to a uniform sample of the archive (see docs/difficulty.md): the mean clear rate of a level is about 0.2
// and the spread between levels is so large that the prior is worth only about two attempts. Three attempts' worth leaves
// every level with 40 or more attempts within about a percentage point of its raw rate.
var Default = Prior{Mean: 0.2, Strength: 3}

// Current is the prior used by the server. It starts as the zero Prior (raw clear rates); the server sets it at start-up
// from SMM_DIFFICULTY_PRIOR. It must not change while the server runs.
var Current Prior

// Score returns the difficulty score, 0..1, for a level with the given clear rate (clears / attempts) and attempts. A level
// without attempts has no score and gets 0.
func (p Prior) Score(rate, attempts float64) float64 {
	if attempts <= 0 {
		return 0
	}

	s := (rate*attempts + p.Mean*p.Strength) / (attempts + p.Strength)

	return math.Max(0, math.Min(1, s))
}

// ScoreCounts is Score from the counts of clears and attempts.
func (p Prior) ScoreCounts(clears, attempts int64) float64 {
	if attempts <= 0 {
		return 0
	}

	return p.Score(float64(clears)/float64(attempts), float64(attempts))
}

// Percent is the score in whole percent, the number the game's difficulty ranking carries.
func (p Prior) Percent(rate, attempts float64) int {
	return int(math.Round(100 * p.Score(rate, attempts)))
}

// PercentCounts is Percent from the counts of clears and attempts.
func (p Prior) PercentCounts(clears, attempts int64) int {
	return int(math.Round(100 * p.ScoreCounts(clears, attempts)))
}

// SQL is the score as an SQL expression over a clear_rate column and an attempts column, with the prior written in as
// numbers (so it needs no query arguments).
func (p Prior) SQL(rateColumn, attemptsColumn string) string {
	return fmt.Sprintf("((%s * %s + %s) / (%s + %s))", rateColumn, attemptsColumn,
		strconv.FormatFloat(p.Mean*p.Strength, 'f', -1, 64), attemptsColumn, strconv.FormatFloat(p.Strength, 'f', -1, 64))
}

// String is the setting that Parse reads back.
func (p Prior) String() string {
	if p.Strength == 0 {
		return "off"
	}

	return strconv.FormatFloat(p.Mean, 'f', -1, 64) + "," + strconv.FormatFloat(p.Strength, 'f', -1, 64)
}

// Parse reads the SMM_DIFFICULTY_PRIOR setting: "" for Default, "off" for raw clear rates, or "MEAN,STRENGTH" such as "0.2,3".
func Parse(s string) (Prior, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	switch s {
	case "":
		return Default, nil
	case "off", "none", "raw", "0":
		return Prior{}, nil
	}

	mean, strength, ok := strings.Cut(s, ",")
	if !ok {
		return Prior{}, fmt.Errorf("SMM_DIFFICULTY_PRIOR %q: write off, or MEAN,STRENGTH such as 0.2,3", s)
	}

	m, err1 := strconv.ParseFloat(strings.TrimSpace(mean), 64)
	k, err2 := strconv.ParseFloat(strings.TrimSpace(strength), 64)

	switch {
	case err1 != nil || err2 != nil:
		return Prior{}, fmt.Errorf("SMM_DIFFICULTY_PRIOR %q: the mean and the strength must be numbers", s)
	case m < 0 || m > 1:
		return Prior{}, fmt.Errorf("SMM_DIFFICULTY_PRIOR %q: the mean is a clear rate between 0 and 1", s)
	case k < 0 || k > 1000:
		return Prior{}, fmt.Errorf("SMM_DIFFICULTY_PRIOR %q: the strength is a number of attempts between 0 and 1000", s)
	}

	return Prior{Mean: m, Strength: k}, nil
}

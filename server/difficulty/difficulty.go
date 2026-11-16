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

// Margin is how many percentage points beyond a requested range a level can still be offered for it: a level whose score is
// further away than this has so little chance of belonging to the range that it is not considered.
const Margin = 25

// Window is the range of scores (whole percent, clamped to 0..100) whose levels may be offered for lo..hi.
func Window(lo, hi int) (int, int) {
	return max(lo-Margin, 0), min(hi+Margin, 100)
}

// InRange is the confidence that a level belongs to the difficulty range lo..hi (whole percent, inclusive, 0..100): the
// probability, under the posterior the score is the mean of, that the level's true clear rate lies in the range. A level
// with many attempts has a narrow posterior, so the answer is close to 0 or 1; a level with few has a wide one, so it
// belongs to a range with a probability below 1 and to its neighbours with a probability above 0. It is the basis of "the
// less we know about a level, the wider the range it can be placed in".
//
// With no prior (Strength 0) there is no posterior and the answer is 1 or 0 by the score alone, as before.
func (p Prior) InRange(rate, attempts float64, lo, hi int) float64 {
	if attempts <= 0 {
		return 0
	}

	if lo <= 0 && hi >= 100 {
		return 1
	}

	if p.Strength <= 0 {
		if s := int(math.Round(100 * p.Score(rate, attempts))); s >= lo && s <= hi {
			return 1
		}

		return 0
	}

	clears := rate * attempts
	a := clears + p.Mean*p.Strength
	b := (attempts - clears) + (1-p.Mean)*p.Strength

	x1, x2 := 0.0, 1.0
	if lo > 0 {
		x1 = (float64(lo) - 0.5) / 100
	}

	if hi < 100 {
		x2 = (float64(hi) + 0.5) / 100
	}

	return math.Max(0, math.Min(1, betaI(a, b, x2)-betaI(a, b, x1)))
}

// betaI is the regularised incomplete beta function I_x(a, b): the probability that a Beta(a, b) variable is at most x.
func betaI(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}

	if x >= 1 {
		return 1
	}

	la, _ := math.Lgamma(a + b)
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	front := math.Exp(la - lb - lc + a*math.Log(x) + b*math.Log(1-x))

	if x < (a+1)/(a+b+2) {
		return front * betaCF(a, b, x) / a
	}

	return 1 - front*betaCF(b, a, 1-x)/b
}

// betaCF evaluates the continued fraction of the incomplete beta function (modified Lentz's method).
func betaCF(a, b, x float64) float64 {
	const (
		tiny = 1e-300
		eps  = 1e-14
	)

	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap

	if math.Abs(d) < tiny {
		d = tiny
	}

	d = 1 / d
	h := d

	for m := 1; m <= 300; m++ {
		fm := float64(m)
		m2 := 2 * fm

		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d

		if math.Abs(d) < tiny {
			d = tiny
		}

		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}

		d = 1 / d
		h *= d * c

		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d

		if math.Abs(d) < tiny {
			d = tiny
		}

		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}

		d = 1 / d
		del := d * c
		h *= del

		if math.Abs(del-1) < eps {
			break
		}
	}

	return h
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

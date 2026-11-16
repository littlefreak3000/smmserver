package difficulty

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTheZeroPriorIsTheRawClearRate(t *testing.T) {
	var off Prior

	for _, c := range []struct{ clears, attempts int64 }{{0, 10}, {3, 3}, {1, 40}, {55, 100}, {7, 300}} {
		if got, want := off.ScoreCounts(c.clears, c.attempts), float64(c.clears)/float64(c.attempts); !near(got, want) {
			t.Errorf("%d/%d: %v, want the raw %v", c.clears, c.attempts, got, want)
		}
	}

	if off.PercentCounts(1, 40) != 3 || off.PercentCounts(55, 100) != 55 {
		t.Errorf("percent %d %d", off.PercentCounts(1, 40), off.PercentCounts(55, 100))
	}
}

func TestThinDataMovesTowardsTheTypicalLevelAndThickDataDoesNot(t *testing.T) {
	p := Default // mean 0.2, strength 3

	// * 3 clears in 3 attempts is not "100 %", and 0 in 5 is not "0 %"
	if got := p.PercentCounts(3, 3); got != 60 {
		t.Errorf("3/3 -> %d %%, want 60", got)
	}

	if got := p.PercentCounts(0, 5); got != 8 {
		t.Errorf("0/5 -> %d %%, want 8", got)
	}

	// * a well-played level keeps its rate: 400 attempts is within a percentage point
	for _, c := range []struct{ clears, attempts int64 }{{4, 400}, {80, 400}, {300, 400}, {2, 50}, {12, 60}} {
		raw := 100 * float64(c.clears) / float64(c.attempts)
		if d := math.Abs(float64(p.PercentCounts(c.clears, c.attempts)) - raw); d > 1.5 {
			t.Errorf("%d/%d moved %.1f points (%v -> %d)", c.clears, c.attempts, d, raw, p.PercentCounts(c.clears, c.attempts))
		}
	}

	// * more evidence at the same rate pulls less: 1/4 and 100/400 are both 25 %
	if !(p.ScoreCounts(100, 400) > p.ScoreCounts(1, 4)) {
		t.Errorf("1/4 -> %v, 100/400 -> %v: the level with more evidence should stay closer to its 25 %%", p.ScoreCounts(1, 4), p.ScoreCounts(100, 400))
	}
}

func TestScoresStayInRangeAndAttemptsAreNeeded(t *testing.T) {
	for _, p := range []Prior{{}, Default, {0, 50}, {1, 50}} {
		for _, c := range []struct{ clears, attempts int64 }{{0, 1}, {1, 1}, {0, 100000}, {100000, 100000}} {
			if s := p.ScoreCounts(c.clears, c.attempts); s < 0 || s > 1 {
				t.Errorf("%v %d/%d -> %v", p, c.clears, c.attempts, s)
			}
		}
	}

	if Default.Score(0.5, 0) != 0 || Default.ScoreCounts(0, 0) != 0 {
		t.Error("a level without attempts must not get a score")
	}
}

func TestParseAndString(t *testing.T) {
	for in, want := range map[string]Prior{"": Default, "off": {}, "OFF": {}, "raw": {}, "0.2,3": {0.2, 3}, " 0.3 , 10 ": {0.3, 10}} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}

	for _, bad := range []string{"3", "0.2", "x,3", "0.2,y", "1.5,3", "-0.1,3", "0.2,-1", "0.2,5000"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) was accepted", bad)
		}
	}

	if s := Default.String(); s != "0.2,3" {
		t.Errorf("String() = %q", s)
	}

	if back, err := Parse(Default.String()); err != nil || back != Default {
		t.Errorf("String does not round-trip: %v %v", back, err)
	}

	if (Prior{}).String() != "off" {
		t.Error("the zero prior should print as off")
	}
}

func TestSQLAgreesWithTheGoScore(t *testing.T) {
	// * the expression is what the catalog queries use, so it must be the same formula
	got := Default.SQL("clear_rate", "attempts")

	if !strings.HasPrefix(got, "((clear_rate * attempts + 0.6") || !strings.HasSuffix(got, ") / (attempts + 3))") {
		t.Errorf("SQL() = %s", got)
	}
}

// numericMass integrates the Beta(a, b) density between x1 and x2 with Simpson's rule: an independent check of betaI.
func numericMass(a, b, x1, x2 float64) float64 {
	la, _ := math.Lgamma(a + b)
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	pdf := func(x float64) float64 {
		if x <= 0 || x >= 1 {
			return 0
		}

		return math.Exp(la - lb - lc + (a-1)*math.Log(x) + (b-1)*math.Log(1-x))
	}

	const n = 200000 // even

	h := (x2 - x1) / n
	sum := pdf(x1+1e-12) + pdf(x2-1e-12)

	for i := 1; i < n; i++ {
		w := 2.0
		if i%2 == 1 {
			w = 4
		}

		sum += w * pdf(x1+float64(i)*h)
	}

	return sum * h / 3
}

func TestBetaIMatchesNumericIntegration(t *testing.T) {
	for _, c := range []struct{ a, b, x1, x2 float64 }{
		{3.6, 2.4, 0.0, 0.5}, {20.6, 380.4, 0.045, 0.055}, {1.6, 24.4, 0.0, 0.05}, {40.6, 160.4, 0.15, 0.25},
		{8.6, 2.4, 0.5, 1.0}, {2, 2, 0.2, 0.8}, {5.5, 40.5, 0.0, 0.2}, {120.6, 80.4, 0.5, 0.7},
	} {
		got := betaI(c.a, c.b, c.x2) - betaI(c.a, c.b, c.x1)

		if want := numericMass(c.a, c.b, c.x1, c.x2); math.Abs(got-want) > 1e-5 {
			t.Errorf("Beta(%v, %v) on [%v, %v]: %.8f, numeric integral %.8f", c.a, c.b, c.x1, c.x2, got, want)
		}
	}

	if betaI(2, 3, 0) != 0 || betaI(2, 3, 1) != 1 {
		t.Error("the ends of the distribution are wrong")
	}
}

func TestInRangeIsConfidence(t *testing.T) {
	p := Default

	// * the ranges of a partition of 0..100 share one level: the masses add up to 1
	for _, c := range []struct{ clears, attempts float64 }{{3, 3}, {1, 25}, {20, 400}, {0, 9}, {150, 300}} {
		sum := 0.0
		for _, r := range [][2]int{{0, 4}, {5, 19}, {20, 49}, {50, 100}} {
			sum += p.InRange(c.clears/c.attempts, c.attempts, r[0], r[1])
		}

		if math.Abs(sum-1) > 1e-6 {
			t.Errorf("%v/%v: the four bands add up to %v, want 1", c.clears, c.attempts, sum)
		}
	}

	// * a well-played level is confidently in its own range and hardly in the next one
	thick := p.InRange(0.12, 600, 5, 19)
	if thick < 0.95 || p.InRange(0.12, 600, 20, 49) > 0.01 {
		t.Errorf("600 attempts at 12 %%: %v in 5-19, %v in 20-49", thick, p.InRange(0.12, 600, 20, 49))
	}

	// * the same rate on few attempts is less sure of its range and spreads into the neighbours
	thin := p.InRange(0.12, 12, 5, 19)
	if !(thin < thick-0.2) || p.InRange(0.12, 12, 20, 49) < 0.05 || p.InRange(0.12, 12, 0, 4) < 0.02 {
		t.Errorf("12 attempts at 12 %%: %v in 5-19 (thick %v), %v in 20-49, %v in 0-4", thin, thick, p.InRange(0.12, 12, 20, 49), p.InRange(0.12, 12, 0, 4))
	}

	// * more attempts at the same rate never lowers the confidence in the range the rate sits well inside
	last := 0.0

	for _, a := range []float64{10, 25, 60, 150, 400, 1000} {
		m := p.InRange(0.12, a, 5, 19)
		if m < last-1e-9 {
			t.Errorf("confidence fell from %v to %v at %v attempts", last, m, a)
		}

		last = m
	}

	// * no attempts, no confidence; the whole scale is always certain
	if p.InRange(0.5, 0, 0, 100) != 0 || p.InRange(0.5, 10, 0, 100) != 1 {
		t.Error("the end cases are wrong")
	}

	// * without a prior there is no softness: the score decides, as before
	if (Prior{}).InRange(0.04, 25, 0, 4) != 1 || (Prior{}).InRange(0.06, 25, 0, 4) != 0 {
		t.Error("with no prior a level is in or out by its rate")
	}
}

func TestWindowCoversTheSoftEdges(t *testing.T) {
	if lo, hi := Window(0, 5); lo != 0 || hi != 5+Margin {
		t.Errorf("Window(0, 5) = %d, %d", lo, hi)
	}

	if lo, hi := Window(50, 100); lo != 50-Margin || hi != 100 {
		t.Errorf("Window(50, 100) = %d, %d", lo, hi)
	}
}

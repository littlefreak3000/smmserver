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

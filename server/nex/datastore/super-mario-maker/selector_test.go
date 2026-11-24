package nex_datastore_super_mario_maker

import (
	"testing"

	"github.com/PretendoNetwork/nex-go/v2/types"
)

func extra(vals ...string) types.List[types.String] {
	var l types.List[types.String]
	for _, v := range vals {
		l = append(l, types.NewString(v))
	}

	return l
}

func TestNormalDoesNotReachDownIntoEasy(t *testing.T) {
	old := NarrowNormal

	t.Cleanup(func() { NarrowNormal = old })

	cases := []struct {
		name     string
		extra    types.List[types.String]
		narrow   bool
		min, max int64
	}{
		{"Easy", extra("1", "0", "34", "", "0"), true, 66, 100},
		{"Normal, narrowed", extra("1", "0", "74", "", "0"), true, 26, 65},
		{"Normal, as the game asks", extra("1", "0", "74", "", "0"), false, 26, 100},
		{"Course World Normal filter", extra("1", "35", "74", "0", "0"), true, 26, 65},
		{"Expert", extra("1", "75", "95", "", "0"), true, 5, 25},
		{"Super Expert", extra("1", "96", "100", "", "0"), true, 0, 4},
		{"no filter", extra("0", "", "", "", "0"), true, 0, 100},
		{"not a number", extra("1", "x", "74", "", "0"), true, 0, 100},
	}

	for _, c := range cases {
		NarrowNormal = c.narrow

		if lo, hi := recommendedRange(c.extra); lo != c.min || hi != c.max {
			t.Errorf("%s: got %d-%d, want %d-%d", c.name, lo, hi, c.min, c.max)
		}
	}
}

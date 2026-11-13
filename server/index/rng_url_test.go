package index

import "testing"

func TestSeededStreamIsDeterministicAndContextSeparated(t *testing.T) {
	draw := func(seed, ctx string) []int {
		s := NewSeeded(seed, ctx)
		out := make([]int, 20)

		for i := range out {
			out[i] = s.IntN(1000)
		}

		return out
	}

	equal := func(a, b []int) bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}

		return true
	}

	a := draw("Mario Party", "range|300000000|1|49")

	if !equal(a, draw("mario   party ", "range|300000000|1|49")) {
		t.Fatal("case and spacing must not change a seed")
	}

	if equal(a, draw("mario party 2", "range|300000000|1|49")) {
		t.Fatal("different seeds gave the same stream")
	}

	if equal(a, draw("mario party", "range|300000000|50|100")) {
		t.Fatal("different contexts gave the same stream")
	}
}

func TestIntNStaysInRangeAndHandlesEdges(t *testing.T) {
	s := NewSeeded("x", "y")

	if s.IntN(1) != 0 {
		t.Fatal("IntN(1) must be 0")
	}

	counts := make([]int, 7)

	for i := 0; i < 70000; i++ {
		v := s.IntN(7)
		if v < 0 || v > 6 {
			t.Fatalf("out of range: %d", v)
		}

		counts[v]++
	}

	for v, c := range counts {
		if c < 9000 || c > 11000 { // 10,000 expected; a biased reduction would show here
			t.Fatalf("value %d drawn %d times of 70000", v, c)
		}
	}

	defer func() {
		if recover() == nil {
			t.Fatal("IntN(0) must panic")
		}
	}()

	s.IntN(0)
}

func TestCaptureURLRoundTrip(t *testing.T) {
	urls := []string{
		"https://d2sno3mhmk1ekx.cloudfront.net/10.WUP_AMAJ_datastore/ds/1/data/00001829603-00001?Expires=1624143146&Signature=al6e-zFRWGE0DUUfZmjjCGOJxioxLOoupUetDuXYqMW3dOnEjsDVzZS1zlsq4ZaZVqCp~OcarxfnktSDvY6X2dp10XUqB9FCWG5vFZ7MGwKMhVmwtUvjUJTBQQUjeu15OY9xMGt9wq9uLUvAAp5zEeIL1R7n80xocq61N4MbMWO4SVCXFgivp~m0d8MTkxm91FhO4AWGeWwbj2ZqK9ZcXx4RwYy3CPauN4Y6UMYDcmNR4yrDamqaBFh3HLoYrLsNJnClqJegn5GafRyV3I4YrgDHKhKQhSpHNDiBeuqm8A7-Qy8ko946RcpWiIwP7BI5BdjwkgQRtEBE1aNMXv9X7g__&Key-Pair-Id=APKAJUYKVK3BE6ZPNZBQ",
		"https://cdn.example/data/00000000042-00003?Expires=1&Signature=notbase64!!&Key-Pair-Id=K1", // '!' is not in the allowed alphabet
		"https://cdn.example/data/00000000042-00003?Expires=1&Signature=abc&Key-Pair-Id=K1",         // decodes, but not canonically
	}

	for i, u := range urls {
		c, err := parseCapture(u)
		if i == 1 {
			if err == nil {
				t.Fatal("a signature with illegal characters must be rejected")
			}

			continue
		}

		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}

		if got := c.URL(); got != u {
			t.Fatalf("%d: round trip changed the URL\n got  %s\n want %s", i, got, u)
		}
	}

	c, _ := parseCapture(urls[0])
	if c.ID != 1829603 || c.Rev != 1 || c.Expires != 1624143146 || c.KeyPair != "APKAJUYKVK3BE6ZPNZBQ" || c.SigRaw || len(c.Sig) != 256 {
		t.Fatalf("parsed wrong: id %d rev %d exp %d kp %s raw %v siglen %d", c.ID, c.Rev, c.Expires, c.KeyPair, c.SigRaw, len(c.Sig))
	}

	for _, bad := range []string{"", "https://x/y", "https://x/data/1-1?Expires=1&Signature=a&Key-Pair-Id=k"} {
		if _, err := parseCapture(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

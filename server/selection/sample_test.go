package selection_test

import (
	"os"
	"testing"
)

// readSample returns the sample level file the repository does not carry (see ../levelimport/testdata/README.md); tests
// that need it are skipped without it.
func readSample(t *testing.T) []byte {
	t.Helper()

	b, err := os.ReadFile("../levelimport/testdata/sample.bin")
	if os.IsNotExist(err) {
		t.Skip("no sample level file: see ../levelimport/testdata/README.md")
	}

	if err != nil {
		t.Fatal(err)
	}

	return b
}

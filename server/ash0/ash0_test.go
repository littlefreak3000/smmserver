package ash0_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/ash0"
)

func chunks(t *testing.T, bin []byte) [][]byte {
	t.Helper()

	var idx []int
	for i := bytes.Index(bin, []byte("ASH0")); i >= 0; {
		idx = append(idx, i)
		j := bytes.Index(bin[i+1:], []byte("ASH0"))
		if j < 0 {
			break
		}
		i += 1 + j
	}

	if len(idx) != 4 {
		t.Fatalf("found %d ASH0 markers, want 4", len(idx))
	}

	idx = append(idx, len(bin))
	var out [][]byte
	for i := 0; i < 4; i++ {
		out = append(out, bin[idx[i]:idx[i+1]])
	}

	return out
}

func TestDecompressesTheSampleLevel(t *testing.T) {
	bin := fixture(t, "testdata/sample.bin")
	want := fixture(t, "testdata/course_data.cdt")

	c := chunks(t, bin)
	sizes := []int{51200, 86016, 86016, 51200} // thumbnail0, course_data, course_data_sub, thumbnail1

	for i, chunk := range c {
		got, err := ash0.Decompress(chunk)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}

		if len(got) != sizes[i] {
			t.Fatalf("chunk %d decompressed to %d bytes, want %d", i, len(got), sizes[i])
		}

		if i == 1 && !bytes.Equal(got, want) {
			t.Fatal("overworld differs from the original course_data.cdt")
		}
	}
}

func TestRejectsGarbageWithoutHangingOrPanicking(t *testing.T) {
	cases := map[string][]byte{
		"empty":          nil,
		"not ash":        bytes.Repeat([]byte{1}, 64),
		"only header":    append([]byte("ASH0\x30\x00\x10\x00\x00\x00\x00\x20"), make([]byte, 8)...),
		"bad offset":     append([]byte("ASH0\x30\x00\x10\x00\xFF\xFF\xFF\xFF"), make([]byte, 32)...),
		"all ones body":  append([]byte("ASH0\x30\x00\x10\x00\x00\x00\x00\x20"), bytes.Repeat([]byte{0xFF}, 64)...),
		"all zeros body": append([]byte("ASH0\x30\x00\x10\x00\x00\x00\x00\x20"), make([]byte, 64)...),
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v", r)
				}
			}()

			_, _ = ash0.Decompress(in) // an error or odd output is fine; hanging or panicking is not
		})
	}

	// * a truncated real chunk must fail cleanly or decode partially, never panic
	bin := fixture(t, "testdata/sample.bin")
	_, _ = ash0.Decompress(chunks(t, bin)[1][:100])
}

// fixture reads a sample level file. The repository does not carry level files: put a real course file there to run these
// tests (see testdata/README.md); without it they are skipped.
func fixture(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		t.Skip("no sample level file: see testdata/README.md")
	}

	if err != nil {
		t.Fatal(err)
	}

	return b
}

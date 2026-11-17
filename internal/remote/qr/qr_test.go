package qr

import (
	"strings"
	"testing"
)

// Byte capacities of level M from the standard, versions 1 to 20.
var standardCapacity = [maxVersion]int{14, 26, 42, 62, 84, 106, 122, 152, 180, 213, 251, 287, 331, 362, 412, 450, 504, 560, 624, 666}

func TestByteCapacityMatchesTheStandard(t *testing.T) {
	for v := minVersion; v <= maxVersion; v++ {
		if got := byteCapacity(v); got != standardCapacity[v-1] {
			t.Errorf("version %d holds %d bytes, the standard says %d", v, got, standardCapacity[v-1])
		}
	}
}

// The worked example of the standard's tutorials: HELLO WORLD at level M,
// version 1, has these ten error correction words.
func TestReedSolomonKnownVector(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	got := rsRemainder(data, rsDivisor(10))
	if string(got) != string(want) {
		t.Errorf("ecc = %v, want %v", got, want)
	}
	if rem := rsRemainder(append(data, got...), rsDivisor(10)); string(rem) != string(make([]byte, 10)) {
		t.Errorf("a codeword must leave no remainder, got %v", rem)
	}
}

func TestSizeFollowsTheVersion(t *testing.T) {
	for _, tc := range []struct {
		text string
		size int
	}{
		{strings.Repeat("a", 14), 21},
		{strings.Repeat("a", 15), 25},
		{strings.Repeat("a", MaxPayload), 4*18 + 17},
	} {
		m, err := Encode(tc.text)
		if err != nil {
			t.Fatalf("%d bytes: %v", len(tc.text), err)
		}
		if len(m) != tc.size || len(m[0]) != tc.size {
			t.Errorf("%d bytes: %d modules wide, want %d", len(tc.text), len(m), tc.size)
		}
	}
}

func TestPayloadLimit(t *testing.T) {
	if _, err := Encode(strings.Repeat("a", MaxPayload+1)); err != ErrTooLong {
		t.Errorf("513 bytes: %v", err)
	}
}

func TestEncodeIsDeterministicAndKeepsFunctionPatterns(t *testing.T) {
	text := "https://tunnel.example.com/pair#" + strings.Repeat("Ab-_", 5)
	a, err := Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(text)
	for y := range a {
		for x := range a {
			if a[y][x] != b[y][x] {
				t.Fatalf("module %d,%d differs between runs", x, y)
			}
		}
	}
	n := len(a)
	for _, o := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		for dy := range 7 {
			for dx := range 7 {
				ring := max(abs(dx-3), abs(dy-3))
				if got := a[o[1]+dy][o[0]+dx]; got != (ring != 2) {
					t.Fatalf("finder at %v module %d,%d is %v", o, dx, dy, got)
				}
			}
		}
	}
	for i := 8; i < n-8; i++ {
		if a[6][i] != (i%2 == 0) || a[i][6] != (i%2 == 0) {
			t.Fatalf("timing pattern broken at %d", i)
		}
	}
	if !a[n-8][8] {
		t.Error("the dark module is missing")
	}
}

// The format word is a BCH codeword: dividing it by the generator leaves
// nothing, and the second copy equals the first.
func TestFormatWordIsAValidCodeword(t *testing.T) {
	m, _ := Encode("format check")
	n := len(m)
	var first, second int
	pos1 := [15][2]int{{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8}, {7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8}}
	for i, p := range pos1 {
		if m[p[1]][p[0]] {
			first |= 1 << i
		}
	}
	for i := range 8 {
		if m[8][n-1-i] {
			second |= 1 << i
		}
	}
	for i := 8; i < 15; i++ {
		if m[n-15+i][8] {
			second |= 1 << i
		}
	}
	if first != second {
		t.Fatalf("the two format copies differ: %015b and %015b", first, second)
	}
	word := first ^ 0x5412
	if word>>13 != 0 {
		t.Errorf("error correction level bits are %b, level M is 00", word>>13)
	}
	rem := word
	for i := 14; i >= 10; i-- {
		if rem>>i&1 != 0 {
			rem ^= 0x537 << (i - 10)
		}
	}
	if rem != 0 {
		t.Errorf("format word %015b is not a BCH codeword", first)
	}
}

func TestVersionWordFromVersionSeven(t *testing.T) {
	m, _ := Encode(strings.Repeat("v", 140))
	n := len(m) // version 7 holds 122 bytes at level M, so this is version 8
	if n != 8*4+17 {
		t.Fatalf("size %d", n)
	}
	var bits int
	for i := range 18 {
		if m[i/3][n-11+i%3] {
			bits |= 1 << i
		}
	}
	if bits>>12 != 8 {
		t.Errorf("version word says %d", bits>>12)
	}
}

func TestTextRendersEveryModuleRow(t *testing.T) {
	m, _ := Encode("x")
	lines := strings.Split(strings.TrimRight(Text(m), "\n"), "\n")
	if want := (len(m) + 4 + 1) / 2; len(lines) != want {
		t.Errorf("%d lines, want %d", len(lines), want)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n != len(m)+4 {
			t.Errorf("line of %d characters, want %d", n, len(m)+4)
		}
	}
}

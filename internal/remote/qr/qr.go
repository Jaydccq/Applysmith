// Package qr encodes a short text as a QR Code symbol. It supports byte mode
// at error-correction level M, versions 1 to 20, which holds the 512-byte
// pairing payload the remote-access capability allows. It is pure: the same
// text always yields the same matrix.
package qr

import (
	"errors"
	"strings"
)

// MaxPayload is the longest text Encode accepts, in bytes.
const MaxPayload = 512

// ErrTooLong means the text is longer than MaxPayload.
var ErrTooLong = errors.New("qr: the payload is longer than 512 bytes")

const (
	minVersion = 1
	maxVersion = 20
)

// Level M error correction: codewords per block, and number of blocks, for
// versions 1 to 20 (index 0 is version 1).
var (
	eccPerBlock = [maxVersion]int{10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26}
	numBlocks   = [maxVersion]int{1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16}
)

// Encode returns the symbol of text: modules[y][x] is true for a dark module.
// The symbol has no quiet zone.
func Encode(text string) ([][]bool, error) {
	data := []byte(text)
	if len(data) > MaxPayload {
		return nil, ErrTooLong
	}
	ver := 0
	for v := minVersion; v <= maxVersion; v++ {
		if len(data) <= byteCapacity(v) {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrTooLong
	}
	words := interleave(ver, dataCodewords(ver, data))
	b := newBuilder(ver)
	b.drawFunctionPatterns()
	b.drawCodewords(words)
	return b.finish(), nil
}

// rawModules is the number of modules that can hold data or error correction.
func rawModules(ver int) int {
	n := (16*ver+128)*ver + 64
	if ver >= 2 {
		align := ver/7 + 2
		n -= (25*align-10)*align - 55
		if ver >= 7 {
			n -= 36
		}
	}
	return n
}

func dataCapacityWords(ver int) int {
	return rawModules(ver)/8 - eccPerBlock[ver-1]*numBlocks[ver-1]
}

// byteCapacity is how many bytes version ver holds in byte mode: the mode
// indicator and the character count take 12 or 20 bits of the data words.
func byteCapacity(ver int) int {
	head := 4 + 8
	if ver >= 10 {
		head = 4 + 16
	}
	return (dataCapacityWords(ver)*8 - head) / 8
}

func dataCodewords(ver int, data []byte) []byte {
	var bits bitBuffer
	bits.add(0b0100, 4)
	if ver >= 10 {
		bits.add(len(data), 16)
	} else {
		bits.add(len(data), 8)
	}
	for _, c := range data {
		bits.add(int(c), 8)
	}
	capBits := dataCapacityWords(ver) * 8
	bits.add(0, min(4, capBits-len(bits)))
	bits.add(0, (8-len(bits)%8)%8)
	for pad := 0xEC; len(bits) < capBits; pad ^= 0xEC ^ 0x11 {
		bits.add(pad, 8)
	}
	out := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit {
			out[i>>3] |= 1 << (7 - i&7)
		}
	}
	return out
}

type bitBuffer []bool

func (b *bitBuffer) add(val, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, val>>i&1 != 0)
	}
}

// interleave splits the data words into blocks, appends each block's error
// correction words and merges the blocks column by column.
func interleave(ver int, data []byte) []byte {
	blocks, ecc := numBlocks[ver-1], eccPerBlock[ver-1]
	raw := rawModules(ver) / 8
	short := blocks - raw%blocks
	shortLen := raw / blocks
	div := rsDivisor(ecc)
	parts := make([][]byte, blocks)
	k := 0
	for i := range parts {
		n := shortLen - ecc
		if i >= short {
			n++
		}
		dat := data[k : k+n]
		k += n
		block := append([]byte(nil), dat...)
		if i < short {
			block = append(block, 0) // keeps the columns aligned; skipped below
		}
		parts[i] = append(block, rsRemainder(dat, div)...)
	}
	var out []byte
	for i := range parts[0] {
		for j, p := range parts {
			if i != shortLen-ecc || j >= short {
				out = append(out, p[i])
			}
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x1D
		z ^= (y >> i & 1) * x
	}
	return z
}

func rsDivisor(degree int) []byte {
	out := make([]byte, degree)
	out[degree-1] = 1
	root := byte(1)
	for range degree {
		for j := range out {
			out[j] = gfMul(out[j], root)
			if j+1 < degree {
				out[j] ^= out[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return out
}

func rsRemainder(data, div []byte) []byte {
	out := make([]byte, len(div))
	for _, b := range data {
		f := b ^ out[0]
		copy(out, out[1:])
		out[len(out)-1] = 0
		for i := range out {
			out[i] ^= gfMul(div[i], f)
		}
	}
	return out
}

// Rows renders the symbol as one string per row, '1' for a dark module and '0'
// for a light one, without a quiet zone: the shape the dashboard draws.
func Rows(modules [][]bool) []string {
	rows := make([]string, len(modules))
	for y, row := range modules {
		var b strings.Builder
		for _, dark := range row {
			if dark {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return rows
}

// Text renders the symbol with a two-module quiet zone, two rows per line, in
// block characters. Light modules are filled, so the code reads correctly on a
// terminal with light text on a dark background.
func Text(modules [][]bool) string {
	const quiet = 2
	n := len(modules)
	at := func(x, y int) bool { // dark module
		return x >= 0 && y >= 0 && x < n && y < n && modules[y][x]
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		for x := -quiet; x < n+quiet; x++ {
			top, bottom := !at(x, y), !at(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

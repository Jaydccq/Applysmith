package demo

// Advance widths of the standard Type1 fonts Helvetica and Helvetica-Bold, in
// 1/1000 em, for the printable ASCII range. The PDF viewer supplies the glyphs;
// the widths here only decide where lines break and where right-aligned text
// starts, so they must match the font metrics exactly.
var (
	helveticaWidths = [95]int{
		278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
		1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
		333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
		556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
	}
	helveticaBoldWidths = [95]int{
		278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
		975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
		333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
		611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
	}
)

// The WinAnsi bytes the layout writes besides ASCII, with their widths, which
// are the same in both fonts.
const (
	winEnDash = 0x96
	winBullet = 0x95
)

var winAnsiExtra = map[rune]struct {
	code  byte
	width int
}{
	'–': {winEnDash, 556},
	'•': {winBullet, 350},
}

// winAnsi encodes s for a PDF literal string in WinAnsiEncoding. A rune the
// two metric tables do not cover is an error, so a wrong width never hides.
func winAnsi(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch x, ok := winAnsiExtra[r]; {
		case ok:
			out = append(out, x.code)
		case r >= 0x20 && r < 0x7f:
			out = append(out, byte(r))
		default:
			return nil, &unsupportedRuneError{r}
		}
	}
	return out, nil
}

type unsupportedRuneError struct{ r rune }

func (e *unsupportedRuneError) Error() string {
	return "demo: the résumé uses a character outside the supported set: " + string(e.r)
}

// textWidth is the width of s in points at the given size.
func textWidth(s string, size float64, bold bool) float64 {
	table := &helveticaWidths
	if bold {
		table = &helveticaBoldWidths
	}
	w := 0
	for _, r := range s {
		if x, ok := winAnsiExtra[r]; ok {
			w += x.width
		} else if r >= 0x20 && r < 0x7f {
			w += table[r-0x20]
		}
	}
	return float64(w) * size / 1000
}

package documents

import "strings"

// Line diff operations.
const (
	DiffSame = "same"
	DiffAdd  = "add"
	DiffDel  = "del"
)

// DiffLine is one line of a line diff.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// maxDiffLines bounds the table the diff builds; a document is a page or two,
// so a larger input is not a document this product wrote.
const maxDiffLines = 1500

// lineDiff compares two texts line by line with a longest-common-subsequence
// table. Trailing newlines do not make a line.
func lineDiff(old, new string) []DiffLine {
	a, b := splitLines(old), splitLines(new)
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		out := make([]DiffLine, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, DiffLine{DiffDel, l})
		}
		for _, l := range b {
			out = append(out, DiffLine{DiffAdd, l})
		}
		return out
	}
	// lcs[i][j] is the common length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{DiffSame, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{DiffDel, a[i]})
			i++
		default:
			out = append(out, DiffLine{DiffAdd, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, DiffLine{DiffDel, a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, DiffLine{DiffAdd, b[j]})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

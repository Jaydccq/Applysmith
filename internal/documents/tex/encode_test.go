package tex

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestTeXEncode_PercentAndSpecials(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"percent does not comment out the line", "100% reliable", `100\% reliable`},
		{"every special character", `& % $ # _ { } ~ ^ \`, `\& \% \$ \# \_ \{ \} \textasciitilde{} \textasciicircum{} \textbackslash{}`},
		{"command text stays literal", `\input{/etc/hosts} \write18{x}`, `\textbackslash{}input\{/etc/hosts\} \textbackslash{}write18\{x\}`},
		{"directlua stays literal", `\directlua{os.exit()}`, `\textbackslash{}directlua\{os.exit()\}`},
		{"leading bracket cannot open an optional argument", "[Lead] item", `{[}Lead{]} item`},
		{"angle brackets and bar", "a<b>c|d", `a\textless{}b\textgreater{}c\textbar{}d`},
		{"quotes keep their ASCII form", "it's \"x\" `y`", `it\textquotesingle{}s \textquotedbl{}x\textquotedbl{} \textasciigrave{}y\textasciigrave{}`},
		{"line breaks become spaces", "a\nb\r\nc\td", "a b  c d"},
		{"control characters are dropped", "a\x00b\x07c\u200bd", "abcd"},
		{"accents pass through", "Gen\u00e8ve", "Gen\u00e8ve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Fatalf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTeXEncode_NoMarkupSurvives(t *testing.T) {
	const active = `\%$#_{}&~^`
	inputs := []string{`a%b`, `\x`, `{`, `}`, `$x$`, `#1`, `a&b`, `_x_`, `^x`, `~`, `a--b`, `\\\\`, `%{}\`}
	for _, in := range inputs {
		rest := Text(in)
		for _, esc := range escapeValues() {
			rest = strings.ReplaceAll(rest, esc, "")
		}
		rest = strings.ReplaceAll(rest, `\mbox{-}`, "")
		if strings.ContainsAny(rest, active) {
			t.Errorf("Text(%q) leaves markup outside its escapes: %q", in, rest)
		}
	}
}

// escapeValues lists the escape sequences longest first, so removing them from
// an encoded string never leaves half of one behind.
func escapeValues() []string {
	var out []string
	for _, v := range escapes {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func TestTeXEncode_URL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "https://example.com/in/alex", "https://example.com/in/alex"},
		{"query and fragment", "https://example.com/a?x=1&y=2#top", `https://example.com/a?x=1\&y=2\#top`},
		{"underscore", "https://example.com/a_b", `https://example.com/a\_b`},
		{"percent escape", "https://example.com/a%20b", `https://example.com/a\%20b`},
		{"tilde", "https://example.com/~alex", `https://example.com/\%7Ealex`},
		{"mail", "mailto:alex@example.com", "mailto:alex@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := URL(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("URL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestTeXEncode_URLRejected(t *testing.T) {
	for _, in := range []string{
		"", " https://example.com", "javascript:alert(1)", "file:///etc/hosts", "https://", "mailto:",
		"https://example.com/a b", "https://example.com/\u00e9", "ftp://example.com", "example.com/path",
		"https://example.com/" + strings.Repeat("a", 2100), "\\input{x}",
	} {
		if got, err := URL(in); !errors.Is(err, ErrURL) {
			t.Errorf("URL(%q) = %q, %v; want ErrURL", in, got, err)
		}
	}
}

func TestRich(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"cut cost by **40%** in a year", `cut cost by \textbf{40\%} in a year`},
		{`**\input{x}** ran`, `\textbf{\textbackslash{}input\{x\}} ran`},
		{"unpaired ** marker", "unpaired  marker"},
		{"**a** and **b** and **c", "a and b and c"},
		{"plain & simple", `plain \& simple`},
	} {
		if got := Rich(c.in); got != c.want {
			t.Errorf("Rich(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

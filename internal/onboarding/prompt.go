package onboarding

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Prompter is how the interactive flow asks for input. A test or an agent
// drives it with a LinePrompter over its answers; a person gets a terminal one.
type Prompter interface {
	// Ask returns the answer; an empty answer returns def.
	Ask(label, def string) (string, error)
	// Secret reads a value that must not be echoed or printed.
	Secret(label string) (string, error)
}

// ErrScriptExhausted is returned when a script has no answer left.
var ErrScriptExhausted = errors.New("the answers ran out")

// LinePrompter reads one answer per line. Lines starting with # are comments.
// A secret is read from a line too, unless Hidden is set (a terminal).
type LinePrompter struct {
	In  *bufio.Scanner
	Out io.Writer
	// Hidden reads a secret without echo; nil reads the next line.
	Hidden func() (string, error)
}

// NewLinePrompter reads answers from r and echoes prompts to w.
func NewLinePrompter(r io.Reader, w io.Writer) *LinePrompter {
	return &LinePrompter{In: bufio.NewScanner(r), Out: w}
}

func (p *LinePrompter) next(label string) (string, error) {
	for p.In.Scan() {
		line := strings.TrimSpace(p.In.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		return line, nil
	}
	if err := p.In.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w at the question %q", ErrScriptExhausted, label)
}

func (p *LinePrompter) Ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.Out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.Out, "%s: ", label)
	}
	a, err := p.next(label)
	if err != nil {
		fmt.Fprintln(p.Out)
		return "", err
	}
	if a == "" {
		a = def
	}
	if p.Hidden == nil {
		fmt.Fprintln(p.Out, a)
	} else {
		fmt.Fprintln(p.Out)
	}
	return a, nil
}

func (p *LinePrompter) Secret(label string) (string, error) {
	fmt.Fprintf(p.Out, "%s (not shown): ", label)
	var v string
	var err error
	if p.Hidden != nil {
		v, err = p.Hidden()
	} else {
		v, err = p.next(label)
	}
	fmt.Fprintln(p.Out)
	return v, err
}

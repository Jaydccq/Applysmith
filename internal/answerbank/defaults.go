package answerbank

import (
	_ "embed"
	"slices"
	"sync"

	"applysmith/internal/answerbank/match"
)

// defaultBankYAML is the default answer bank. It ships blank except for the
// self-identification entries, which decline, the auto-acknowledge entries
// and the owner's own defaults (relocation assistance whenever the role means
// relocating, no SAT, ACT or GRE score); the product never supplies any other
// fact about the user.
//
//go:embed defaults/answer-bank.yml
var defaultBankYAML []byte

// DefaultBytes returns a copy of the embedded default bank file.
func DefaultBytes() []byte { return append([]byte(nil), defaultBankYAML...) }

var (
	defaultOnce   sync.Once
	defaultResult Result
	defaultErr    error
)

// DefaultBank parses the embedded default bank. The result is a copy: a caller
// may change it without touching what the next call returns.
func DefaultBank() (Result, error) {
	defaultOnce.Do(func() { defaultResult, defaultErr = parse("defaults/answer-bank.yml", defaultBankYAML, nil) })
	r := defaultResult
	r.Bank = r.Bank.Clone()
	r.Warnings = slices.Clone(r.Warnings)
	return r, defaultErr
}

// withDefaults is res, the bank loaded from the file's bytes data, with the
// shipped defaults applied in memory (flow round 11): the default entries
// whose id the file lacks follow its own, and a default entry the file holds
// takes the default's examples or input types where every item of its list is
// the default's own, and the default's answer where it is still a blank
// placeholder of the default's kind, held for review when the default's is.
// Nothing the user wrote changes, res is left as it is, and changed names
// every entry the defaults changed or added.
func withDefaults(res Result, data []byte) (out Result, changed map[string]bool, err error) {
	have, _, err := fileEntryIDs(data)
	if err != nil {
		return res, nil, err
	}
	def, err := DefaultBank()
	if err != nil {
		return res, nil, err
	}
	out, changed = res, map[string]bool{}
	out.Bank = res.Bank.Clone()
	at := map[string]int{}
	for i, e := range out.Bank.Entries {
		if _, dup := at[e.ID]; !dup {
			at[e.ID] = i
		}
	}
	for _, d := range def.Bank.Entries {
		i, loaded := at[d.ID]
		switch {
		case !have[d.ID]:
			d.Shipped = true
			out.Bank.Entries = append(out.Bank.Entries, d)
			changed[d.ID] = true
		case loaded && refresh(&out.Bank.Entries[i], d):
			changed[d.ID] = true
		}
	}
	return out, changed, nil
}

// refresh gives e, the file's copy of default entry d, what d ships newer, as
// withDefaults says, and reports whether e changed.
func refresh(e *match.Entry, d match.Entry) bool {
	n := false
	for _, l := range []struct {
		mine *[]string
		want []string
	}{{&e.Examples, d.Examples}, {&e.InputTypes, d.InputTypes}} {
		if len(l.want) == 0 || slices.Equal(*l.mine, l.want) || slices.ContainsFunc(*l.mine, func(x string) bool { return !slices.Contains(l.want, x) }) {
			continue
		}
		*l.mine, n = slices.Clone(l.want), true
	}
	if e.Placeholder && e.BodyEmpty() && e.Kind == d.Kind && !d.Placeholder && !d.BodyEmpty() {
		e.Body, e.Placeholder, e.Shipped, n = d.Body, false, true, true
		e.NeedsReview = e.NeedsReview || d.NeedsReview
	}
	return n
}

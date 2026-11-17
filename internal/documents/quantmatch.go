package documents

import "math"

// supported reports whether a claim appears in the corpus with the same value
// (or the same vague word), the same unit, rate and approximation and the same
// subject. Anything else is unsupported.
func supported(q quantity, c corpus) bool {
	for _, e := range c.qs {
		if sameQuantity(q, e, c.vocab, c.summary) {
			return true
		}
	}
	return false
}

func sameQuantity(q, e quantity, vocab map[string]bool, summary bool) bool {
	if q.vague != e.vague || q.per != e.per || !modsAgree(q.mod, e.mod) {
		return false
	}
	if q.vague == "" {
		switch {
		case near(q.value, e.value):
			if !unitsAgree(q, e) {
				return false
			}
		case !shareOf(q, e) && !shareOf(e, q):
			return false
		}
	} else if !unitsAgree(q, e) {
		return false
	}
	return sameSubject(q, e, vocab, summary)
}

// modsAgree: an approximation is part of the figure. Dropping a "plus" only
// weakens a claim; dropping an "about" or "nearly" does not.
func modsAgree(q, e string) bool {
	return q == e || (e == ">" && q == "")
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// shareOf is true when f is a fraction ("half") and p states the same share in
// percent ("50%").
func shareOf(f, p quantity) bool {
	return f.vague == "" && f.unit == "x" && f.value < 1 && p.unit == "%" && near(f.value*100, p.value)
}

// unitsAgree: equal units, a claim without a unit against a counted evidence
// unit, or a counted noun that the other side names right after its figure
// ("4 junior engineers" holds "4 engineers"). A measure never agrees with no
// unit, so "40" is not "40%".
func unitsAgree(q, e quantity) bool {
	switch {
	case e.unit == q.unit:
		return true
	case q.unit == "":
		return counted(e.unit)
	case e.unit == "":
		return false
	}
	return (counted(q.unit) && e.post[q.unit]) || (counted(e.unit) && q.post[e.unit])
}

// sameSubject requires the verb class of the claim in the evidence line, the
// same head noun in the evidence's clause, and the same direction of change.
// A modifier the claim adds is allowed only when the evidence line or the
// user's sources use the word. A claim that names no subject only matches a
// counted unit or evidence that names none either; in a summary, which states
// results without room for their subject, it matches any evidence line that
// holds the same figure and the claim's verb class.
func sameSubject(q, e quantity, vocab map[string]bool, summary bool) bool {
	if q.dir != 0 && e.dir != 0 && q.dir != e.dir {
		return false
	}
	for _, v := range q.verbs {
		if !e.line["v:"+v] {
			return false
		}
	}
	if counted(q.unit) {
		return true
	}
	if q.head == "" {
		return e.head == "" || (summary && (len(q.verbs) > 0 || q.dir != 0 && q.dir == e.dir))
	}
	if q.head != e.head {
		return false
	}
	for _, m := range q.mods {
		if !e.ctx[m] && !e.line[m] && !vocab[m] {
			return false
		}
	}
	return true
}

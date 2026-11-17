package match

import "slices"

// Clone returns a copy of the bank that shares no memory with the receiver:
// a caller may change any entry, list, pointer or map of the copy without
// touching the original. Compiled patterns are immutable and stay shared.
func (b Bank) Clone() Bank {
	out := b
	if b.Employers != nil {
		out.Employers = make([]Employer, len(b.Employers))
		for i, e := range b.Employers {
			out.Employers[i] = Employer{Name: e.Name, Aliases: entryCloneStrings(e.Aliases)}
		}
	}
	if b.Entries != nil {
		out.Entries = make([]Entry, len(b.Entries))
		for i, e := range b.Entries {
			out.Entries[i] = e.Clone()
		}
	}
	if b.Overrides != nil {
		out.Overrides = make(map[string][]Override, len(b.Overrides))
		for host, list := range b.Overrides {
			cp := make([]Override, len(list))
			for i, o := range list {
				cp[i] = o.Clone()
			}
			out.Overrides[host] = cp
		}
	}
	if b.HostSpelling != nil {
		out.HostSpelling = make(map[string]string, len(b.HostSpelling))
		for k, v := range b.HostSpelling {
			out.HostSpelling[k] = v
		}
	}
	return out
}

// Clone returns a copy of the override that shares no memory with it.
func (o Override) Clone() Override {
	out := o
	out.Body = o.Body.clone()
	out.Fields = entryCloneStrings(o.Fields)
	return out
}

// Clone returns a copy of the entry that shares no memory with the receiver.
func (e Entry) Clone() Entry {
	out := e
	out.Body = e.Body.clone()
	out.Meta = e.Meta.clone()
	out.NegationPatterns = slices.Clone(e.NegationPatterns)
	return out
}

func (b Body) clone() Body {
	out := b
	out.Preferred = entryCloneStrings(b.Preferred)
	out.ProficiencyOptions = entryCloneStrings(b.ProficiencyOptions)
	out.Selected = entryCloneStrings(b.Selected)
	out.AbstainIfAmbiguous = clonePtr(b.AbstainIfAmbiguous)
	return out
}

func (m Meta) clone() Meta {
	out := m
	out.InputTypes = entryCloneStrings(m.InputTypes)
	out.Confidence = clonePtr(m.Confidence)
	out.ConfidenceFloor = clonePtr(m.ConfidenceFloor)
	out.Examples = entryCloneStrings(m.Examples)
	out.ExceptionTerms = entryCloneStrings(m.ExceptionTerms)
	out.QuestionNegationTerms = entryCloneStrings(m.QuestionNegationTerms)
	if t := m.NumericThreshold; t != nil {
		cp := *t
		cp.MaxValue = clonePtr(t.MaxValue)
		out.NumericThreshold = &cp
	}
	if lp := m.LocationPolicy; lp != nil {
		cp := *lp
		if r := lp.Resolved; r != nil {
			rp := *r
			rp.Places = entryCloneStrings(r.Places)
			rp.Excluded = entryCloneStrings(r.Excluded)
			cp.Resolved = &rp
		}
		out.LocationPolicy = &cp
	}
	if m.CapturedScope != nil {
		out.CapturedScope = make([]ScopeItem, len(m.CapturedScope))
		for i, s := range m.CapturedScope {
			out.CapturedScope[i] = ScopeItem{Host: s.Host, Question: s.Question, Options: entryCloneStrings(s.Options)}
		}
	}
	out.AllowedOptions = entryCloneStrings(m.AllowedOptions)
	out.SkillTerms = entryCloneStrings(m.SkillTerms)
	out.SkillBlacklistTerms = entryCloneStrings(m.SkillBlacklistTerms)
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

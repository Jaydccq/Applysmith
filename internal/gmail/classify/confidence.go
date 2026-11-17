package classify

import "math"

// confidenceFeatures are the facts the score is built from.
type confidenceFeatures struct {
	trustedSender, explicitCompany, explicitRole bool
	hardEventPhrase, weakEventPhrase             bool
	subjectMatch                                 bool
	companyStrategy                              string
	roleStrippedPrefix                           bool
	eventType                                    string
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// score starts at 0.20, is raised by a trusted sender, an explicit employer, an
// explicit role, a hard event phrase and a matching subject, lowered by an
// employer derived only from the sender domain, an unknown employer on a
// receipt and a stripped role prefix, and clamped to [0, 1] with two decimals.
func (f confidenceFeatures) score() float64 {
	s := 0.2
	add := func(ok bool, d float64) {
		if ok {
			s += d
		}
	}
	add(f.trustedSender, 0.3)
	add(f.explicitCompany, 0.2)
	add(f.explicitRole, 0.15)
	add(f.hardEventPhrase, 0.15)
	add(f.weakEventPhrase && !f.hardEventPhrase, 0.05)
	add(f.subjectMatch, 0.1)
	add(f.companyStrategy == StratSubject || f.companyStrategy == StratATSName, 0.05)
	add(f.companyStrategy == StratSenderDomain, -0.15)
	add(f.companyStrategy == StratUnknown && f.eventType == TypeApplied, -0.2)
	add(f.roleStrippedPrefix, -0.1)
	return math.Max(0, math.Min(1, round2(s)))
}

func (f confidenceFeatures) breakdown(co CompanyEvidence, role RoleEvidence) Confidence {
	company := co.Confidence
	if f.companyStrategy == StratUnknown {
		company = 0
	}
	base := map[string]float64{"subject": 0.8, "body": 0.7, "fallback_keyword": 0.5}[role.Strategy]
	if role.StrippedPrefix {
		base = math.Max(0, base-0.2)
	}
	event := 0.8
	switch {
	case f.hardEventPhrase:
		event = math.Min(1, event+0.1)
	case !f.weakEventPhrase:
		event = math.Max(0, event-0.2)
	}
	trust := 0.5
	if f.trustedSender {
		trust = 0.9
	}
	return Confidence{Overall: f.score(), Event: round2(event), Company: round2(company), Role: round2(base), SenderTrust: trust}
}

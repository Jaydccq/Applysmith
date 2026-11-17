package classify

// corporateSuffixes may follow an employer's name in its legal form and never
// in its domain name.
var corporateSuffixes = []string{
	"inc", "corp", "corporation", "co", "company", "llc", "ltd", "limited", "plc", "group", "holdings",
	"labs", "technologies", "technology", "systems", "software",
}

// anchoredSender reports that something outside the message vouches for its
// sender: the taxonomy lists the domain as a trusted or vendor ATS, or the
// employer named by the message's own explicit sources is the sender's domain.
// An employer taken from the sender's domain is circular and proves nothing, a
// webmail or scheduling-tool domain belongs to everyone, and a name, an address
// or body text only corroborates a sender that is already anchored.
func (t *Taxonomy) anchoredSender(p Policy, co CompanyEvidence) bool {
	if p.Anchored {
		return true
	}
	if p.Webmail || p.SchedulingTool || p.Restricted || p.Domain == "" {
		return false
	}
	switch co.Strategy {
	case StratUnknown, StratSenderDomain, StratCoRecipient, "":
		return false
	}
	root, _ := registrableRoot(p.Domain)
	return sameEmployer(slugOf(root), slugOf(co.Value))
}

// sameEmployer compares a domain root with an employer name, both reduced to
// lowercase letters and digits, allowing one corporate suffix on either side.
func sameEmployer(root, employer string) bool {
	if len(root) < 3 || len(employer) < 3 {
		return false
	}
	if root == employer {
		return true
	}
	for _, suf := range corporateSuffixes {
		if employer == root+suf || root == employer+suf {
			return true
		}
	}
	return false
}

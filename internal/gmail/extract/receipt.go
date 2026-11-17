package extract

import (
	"net/url"
	"regexp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// ReceiptLookup identifies one submission.
type ReceiptLookup struct {
	JobURL  string
	ATS     string
	Tenant  string
	Company string
}

// ReceiptMessage is the part of a message the scorer reads.
type ReceiptMessage struct {
	From, Subject, Body string
}

// atsSenderDomains are the public sender domains of the systems whose receipts
// the chain waits for. They rank candidates and never gate them.
var atsSenderDomains = map[string][]string{
	"ashby":      {"ashbyhq.com"},
	"greenhouse": {"greenhouse-mail.io", "greenhouse.io"},
	"lever":      {"hire.lever.co", "lever.co"},
	"workday":    {"myworkday.com", "myworkdayjobs.com", "workday.com"},
	"linkedin":   {"linkedin.com"},
}

// vendorRegistrable are registrable domains that mail for many employers and so
// can never be a posting's own domain.
var vendorRegistrable = map[string]bool{
	"icims.com": true, "smartrecruiters.com": true, "jobvite.com": true, "successfactors.com": true, "sapsf.com": true,
	"taleo.net": true, "oraclecloud.com": true, "bamboohr.com": true, "workable.com": true, "workablemail.com": true,
	"ashbyhq.com": true, "greenhouse.io": true, "greenhouse-mail.io": true, "lever.co": true, "myworkdayjobs.com": true,
	"myworkday.com": true, "workday.com": true, "linkedin.com": true, "dayforcehcm.com": true, "paylocity.com": true,
	"recruitee.com": true, "teamtailor.com": true, "breezy.hr": true, "brassring.com": true,
}

// nonIdentifying are labels that identify nobody: top-level domains, generic
// labels of hosted careers sites, ATS vendor names and data-centre labels. A
// token every sender address contains would make any employer's receipt look
// like this one, and confirming the wrong receipt is worse than confirming none.
var nonIdentifying = map[string]bool{
	"com": true, "net": true, "org": true, "edu": true, "gov": true, "www": true, "mail": true, "email": true, "inc": true,
	"llc": true, "ltd": true, "corp": true, "the": true, "jobs": true, "job": true, "career": true, "careers": true,
	"apply": true, "talent": true, "hire": true, "hiring": true, "recruiting": true, "people": true,
	"myworkdayjobs": true, "myworkday": true, "workday": true, "oraclecloud": true, "taleo": true, "icims": true,
	"successfactors": true, "sapsf": true, "greenhouse": true, "lever": true, "ashbyhq": true, "ashby": true,
	"smartrecruiters": true, "jobvite": true, "brassring": true, "avature": true, "workable": true, "bamboohr": true,
}

var dataCentreRE = lazyregexp.New(`^(?:wd\d+|us\d+|eu\d+|fa)$`)

// lookupOnlyReceipt are wordings the lookup accepts beyond the classifier's
// shared list. They are too loose for classification ("your application for"
// opens rejection mail too) but safe here, where an identity signal is also
// required.
var lookupOnlyReceipt = lazyregexp.New(`(?i)\bapplication (?:was |has been )?(?:received|submitted|complete|sent)\b|\byour application (?:to|for|with|has|was sent)\b|\bsuccessfully (?:applied|submitted)\b`)

// ReceiptTokens are the identity tokens of a lookup: words of at least three
// letters from the tenant and the company, without generic and vendor labels.
func ReceiptTokens(vals ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		for _, tok := range strings.FieldsFunc(strings.ToLower(v), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
			if len(tok) < 3 || nonIdentifying[tok] || dataCentreRE.MatchString(tok) || seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

func hasReceiptWording(text string) bool {
	for _, p := range ReceiptPhrases {
		if !strings.HasPrefix(p.ID, "applied.status.") && p.Re.MatchString(text) {
			return true
		}
	}
	return lookupOnlyReceipt.MatchString(text)
}

func registrable(host string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(host), "www."), ".")
	if len(parts) < 2 {
		return strings.Join(parts, ".")
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// addressedToInBody is the employer named as the recipient of this
// application: within 40 characters after an applying or interest phrase.
func addressedToInBody(body string, identity []string) bool {
	text := strings.ToLower(body)
	for _, tok := range identity {
		re := regexp.MustCompile(`(?:appl(?:y|ied|ying|ication)|interest\s+in)\s+(?:to|at|for|with|in)?\s*[^.\n]{0,40}` + regexp.QuoteMeta(tok))
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// ScoreReceipt says how strongly a message belongs to this submission; zero
// means it does not. A message needs receipt vocabulary and an identity signal:
// the tenant or company token in its sender or subject, or the posting's own
// non-ATS domain as sender, or the employer named in the body right after an
// applying phrase. The ATS sender domain alone is no identity (the same
// no-reply address mails every applicant); it adds a point and breaks ties.
func ScoreReceipt(l ReceiptLookup, m ReceiptMessage) int {
	if !hasReceiptWording(m.Subject + "\n" + m.Body) {
		return 0
	}
	from, subject := strings.ToLower(m.From), strings.ToLower(m.Subject)
	domains := atsSenderDomains[strings.ToLower(l.ATS)]
	own := false
	if u, err := url.Parse(l.JobURL); err == nil && u.Hostname() != "" {
		reg := registrable(u.Hostname())
		isATS := vendorRegistrable[reg]
		for _, d := range domains {
			isATS = isATS || d == reg
		}
		own = reg != "" && !isATS && strings.Contains(from, reg)
	}
	identity := ReceiptTokens(l.Tenant, l.Company)
	idToken := false
	for _, t := range identity {
		if strings.Contains(from, t) || strings.Contains(subject, t) {
			idToken = true
			break
		}
	}
	body := !idToken && !own && addressedToInBody(m.Body, identity)
	if !idToken && !own && !body {
		return 0
	}
	score := 2
	if idToken && own {
		score++
	}
	for _, d := range domains {
		if strings.Contains(from, "@"+d) || strings.Contains(from, "."+d) {
			score++
			break
		}
	}
	return score
}

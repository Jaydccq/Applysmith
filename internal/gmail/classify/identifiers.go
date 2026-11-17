package classify

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

const uuidSrc = `[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}`

var workdayHostRE = lazyregexp.New(`(?i)\b[a-z0-9-]+\.(?:wd\d+\.)?myworkdayjobs\.com\b`)

// urlRule reads an identifier out of a URL on one known ATS host. Group
// numbers say which capture is which.
type urlRule struct {
	source, ats      string
	re               *lazyregexp.Regexp
	job, req, tenant int
	tenantStrip      *lazyregexp.Regexp
	gate             *lazyregexp.Regexp
}

var urlRules = []urlRule{
	{source: "greenhouse_boards_url", ats: "greenhouse", re: lazyregexp.New(`(?i)\b(?:boards|job-boards)\.greenhouse\.io/([a-z0-9-]+)/jobs/(\d{3,})`), tenant: 1, job: 2},
	{source: "greenhouse_gh_jid", ats: "greenhouse", re: lazyregexp.New(`(?i)\bgh_jid=(\d{3,})`), job: 1},
	{source: "lever_url", ats: "lever", re: lazyregexp.New(`(?i)\bjobs\.lever\.co/([a-z0-9-]+)/(` + uuidSrc + `)\b`), tenant: 1, job: 2},
	{source: "ashby_url", ats: "ashby", re: lazyregexp.New(`(?i)\bjobs\.ashbyhq\.com/([a-z0-9](?:[a-z0-9-]|%20)*)/(` + uuidSrc + `)\b`), tenant: 1, job: 2},
	{source: "workday_url", ats: "workday", gate: workdayHostRE, re: lazyregexp.New(`(?i)[?&]jobReqId=([A-Za-z0-9_-]{3,20})\b`), req: 1},
	// A bare requisition number is trusted only beside a Workday tenant URL.
	{source: "workday_url", ats: "workday", gate: workdayHostRE, re: lazyregexp.New(`(?i)\b((?:R|JR)[-_]\d{3,})\b`), req: 1},
	{source: "icims_url", ats: "icims", re: lazyregexp.New(`(?i)\b([a-z0-9-]+)\.icims\.com/jobs/(\d{3,})\b`), tenant: 1, tenantStrip: lazyregexp.New(`^(?:jobs|careers)-`), job: 2},
	{source: "smartrecruiters_url", ats: "smartrecruiters", re: lazyregexp.New(`(?i)\bjobs\.smartrecruiters\.com/([a-z0-9-]+)/(\d{6,})`), tenant: 1, job: 2},
	{source: "jobvite_url", ats: "jobvite", re: lazyregexp.New(`(?i)\bjobs\.jobvite\.com/([a-z0-9-]+)/job/([a-z0-9]{5,20})\b`), tenant: 1, job: 2},
	{source: "successfactors_url", ats: "successfactors", re: lazyregexp.New(`(?i)\bsuccessfactors\.(?:com|eu)\S*?(?:[?&]|&amp;)(?:career_job_req_id|jobId)=(\d{3,})`), req: 1},
}

// Labeled text must carry a digit, which keeps "Requisition ID: needed" from
// minting an identifier out of an English word.
const idValue = `([A-Za-z0-9][A-Za-z0-9+_-]{3,19})`

var labeledRules = []struct {
	field string
	re    *lazyregexp.Regexp
}{
	{"req", lazyregexp.New(`(?i)\b(?:job\s+requisition|requisition|req)(?:\s+(?:id|number|no\.?))?[\s:#]+` + idValue)},
	{"job", lazyregexp.New(`(?i)\bjob\s+(?:id|number|#)[\s:#]*` + idValue)},
	{"application", lazyregexp.New(`(?i)\bapplication\s+(?:id|number)[\s:#]*` + idValue)},
}

// extractIdentifiers reads posting identifiers only from known ATS hosts or
// explicitly labeled text. A URL identifier wins over a conflicting labeled
// one. It returns nil when nothing confident is found.
func extractIdentifiers(subject, body, raw string) *Identifiers {
	var texts []string
	for _, t := range []string{subject, body, raw} {
		if t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	var job, app, req, ats, tenant, source string
	grp := func(m []string, i int) string {
		if i <= 0 || i >= len(m) {
			return ""
		}
		return strings.TrimSpace(m[i])
	}
	for _, rule := range urlRules {
		if (rule.job > 0 && job != "") || (rule.req > 0 && req != "") {
			continue
		}
		if rule.gate != nil {
			ok := false
			for _, t := range texts {
				if rule.gate.MatchString(t) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		for _, t := range texts {
			m := rule.re.FindStringSubmatch(t)
			if m == nil {
				continue
			}
			if v := grp(m, rule.job); v != "" && job == "" {
				job = v
			}
			if v := grp(m, rule.req); v != "" && req == "" {
				req = v
			}
			if v := grp(m, rule.tenant); v != "" && tenant == "" {
				if rule.tenantStrip != nil {
					v = rule.tenantStrip.ReplaceAllString(v, "")
				}
				tenant = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(v, "%20", " ")))
			}
			if ats == "" {
				ats = rule.ats
			}
			if source == "" {
				source = rule.source
			}
			break
		}
	}
	for _, rule := range labeledRules {
		if (rule.field == "job" && job != "") || (rule.field == "req" && req != "") || (rule.field == "application" && app != "") {
			continue
		}
		for _, t := range texts {
			m := rule.re.FindStringSubmatch(t)
			if m == nil || !strings.ContainsAny(m[1], "0123456789") {
				continue
			}
			switch rule.field {
			case "job":
				job = m[1]
			case "req":
				req = m[1]
			default:
				app = m[1]
			}
			if source == "" {
				source = "labeled_text"
			}
			break
		}
	}
	if job == "" && app == "" && req == "" {
		return nil
	}
	return &Identifiers{JobID: job, ApplicationID: app, ReqID: req, ATS: ats, Tenant: tenant, Source: source}
}

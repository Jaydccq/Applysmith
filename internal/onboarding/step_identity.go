package onboarding

import (
	"context"
	"encoding/json"
	"net/mail"
	"net/url"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

const askMe = "@ask-me"

var identityFields = []Field{
	{Name: "full_name", Type: "string", Doc: "Full legal name; typed verbatim into application forms and used for typed signatures."},
	{Name: "preferred_name", Type: "string", Doc: "Optional preferred name."},
	{Name: "email", Type: "string", Doc: "Contact email; must be a real address, not a reserved example domain."},
	{Name: "phone", Type: "string", Doc: "Phone number in international form (+ and digits); a US, Canadian or UK national number is converted."},
	{Name: "city", Type: "string", Doc: "City of residence."},
	{Name: "region", Type: "string", Doc: "State, province or region of residence."},
	{Name: "country", Type: "string", Doc: "Country of residence: its name or two-letter code, for example United States or US."},
	{Name: "county", Type: "string", Doc: "Optional county."},
	{Name: "street_address", Type: "string", Doc: "Street address, or " + askMe + " to leave the field for yourself."},
	{Name: "postal_code", Type: "string", Doc: "Postal code, or " + askMe + " to leave the field for yourself."},
	{Name: "date_of_birth", Type: "string", Doc: "Optional, YYYY-MM-DD; used only to answer age questions."},
	{Name: "linkedin", Type: "string", Doc: "Optional https URL on linkedin.com."},
	{Name: "github", Type: "string", Doc: "Optional https URL on github.com."},
	{Name: "portfolio", Type: "string", Doc: "Optional https URL."},
	{Name: "other_links", Type: "string-list", Doc: "Optional additional https URLs."},
}

type identityInput struct {
	FullName      string   `json:"full_name"`
	PreferredName string   `json:"preferred_name"`
	Email         string   `json:"email"`
	Phone         string   `json:"phone"`
	City          string   `json:"city"`
	Region        string   `json:"region"`
	Country       string   `json:"country"`
	County        string   `json:"county"`
	Street        string   `json:"street_address"`
	Postal        string   `json:"postal_code"`
	DateOfBirth   string   `json:"date_of_birth"`
	LinkedIn      string   `json:"linkedin"`
	GitHub        string   `json:"github"`
	Portfolio     string   `json:"portfolio"`
	OtherLinks    []string `json:"other_links"`
}

func scopeIdentity(ctx context.Context, env *Env) (string, error) {
	return profileScope("candidate", "links")(ctx, env)
}

// normalizePhone returns the number in international form. A national number
// is converted only for the countries whose trunk rules are simple.
func normalizePhone(phone, country string) (string, bool) {
	var b strings.Builder
	for _, r := range phone {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case strings.ContainsRune(" -(). ", r):
		default:
			return "", false
		}
	}
	s := b.String()
	switch {
	case strings.HasPrefix(s, "00"):
		s = "+" + s[2:]
	case strings.HasPrefix(s, "+"):
	case (country == "US" || country == "CA") && len(s) == 10:
		s = "+1" + s
	case country == "GB" && strings.HasPrefix(s, "0") && len(s) >= 10:
		s = "+44" + s[1:]
	default:
		return "", false
	}
	if n := len(s) - 1; n < 8 || n > 15 {
		return "", false
	}
	return s, true
}

func httpsOn(raw, domain string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return domain == "" || h == domain || strings.HasSuffix(h, "."+domain)
}

func applyIdentity(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in identityInput
	if err := decodeInput(raw, identityFields, &in); err != nil {
		return Applied{}, err
	}
	p, _, bad := profileData(env)
	if bad != nil && bad.Status == StepMissing {
		return Applied{}, &EnvError{Message: bad.Reasons[0].Text}
	}
	fe := FieldErrors{}
	country := ""
	if c := strings.TrimSpace(in.Country); c != "" {
		code, ok := match.CountryCode(c)
		if !ok {
			fe.add("country", "is not a country this app knows; enter its two-letter code, for example US or GB")
		}
		country = code
	}
	var ops []workspace.Op
	str := func(path, v string) {
		if v = strings.TrimSpace(v); v != "" {
			ops = append(ops, setOp(path, v))
		}
	}
	str("candidate.full_name", in.FullName)
	str("candidate.preferred_name", in.PreferredName)
	if e := strings.TrimSpace(in.Email); e != "" {
		a, err := mail.ParseAddress(e)
		switch {
		case err != nil || a.Address != e || !strings.Contains(emailDomain(e), "."):
			fe.add("email", "is not a valid email address")
		case reservedEmail(e):
			fe.add("email", "is on a reserved example domain; enter your own address")
		default:
			ops = append(ops, setOp("candidate.email", e))
		}
	}
	if ph := strings.TrimSpace(in.Phone); ph != "" {
		effective := country
		if effective == "" {
			effective = strings.ToUpper(p.Candidate.Address.Country)
		}
		if n, ok := normalizePhone(ph, effective); ok {
			ops = append(ops, setOp("candidate.phone", n))
		} else {
			fe.add("phone", "cannot be read as a phone number; enter it in international form, for example +14155550123")
		}
	}
	str("candidate.address.city", in.City)
	str("candidate.address.region", in.Region)
	str("candidate.address.county", in.County)
	str("candidate.address.line1", in.Street)
	str("candidate.address.postal_code", in.Postal)
	if country != "" {
		ops = append(ops, setOp("candidate.address.country", country))
	}
	str("candidate.date_of_birth", in.DateOfBirth)
	for _, l := range []struct{ field, path, v, domain string }{
		{"linkedin", "links.linkedin", in.LinkedIn, "linkedin.com"},
		{"github", "links.github", in.GitHub, "github.com"},
		{"portfolio", "links.portfolio", in.Portfolio, ""},
	} {
		switch v := strings.TrimSpace(l.v); {
		case v == "":
		case !httpsOn(v, l.domain):
			msg := "must be an https URL"
			if l.domain != "" {
				msg += " on " + l.domain
			}
			fe.add(l.field, msg)
		default:
			ops = append(ops, setOp(l.path, v))
		}
	}
	if len(in.OtherLinks) > 0 {
		for i, v := range in.OtherLinks {
			if !httpsOn(strings.TrimSpace(v), "") {
				fe.add("other_links", "entry "+itoa(i+1)+" must be an https URL")
			}
		}
		ops = append(ops, setOp("links.other", in.OtherLinks))
	}
	if len(fe) > 0 {
		return Applied{}, fe
	}
	changed, err := editProfile(env, ops, dry)
	if err != nil {
		return Applied{}, err
	}
	return Applied{Step: "identity", DryRun: dry, Wrote: changed && !dry, Detection: Evaluate(ctx, env, mustStep("identity")),
		Notes: []string{"The full name is typed verbatim into application forms and used for typed signatures."}}, nil
}

func detectIdentity(_ context.Context, env *Env) Detection {
	p, _, bad := profileData(env)
	if bad != nil {
		return *bad
	}
	c := p.Candidate
	var rs []Reason
	blank := func(field, label, v string) bool {
		if strings.TrimSpace(v) == "" {
			rs = append(rs, reasonf(ReasonBlank, field, label+" is empty"))
			return true
		}
		return false
	}
	if !blank("candidate.full_name", "full name", c.FullName) && isPersona(c.FullName) {
		rs = append(rs, reasonf(ReasonPersona, "candidate.full_name", "full name is the example name "+c.FullName))
	}
	if !blank("candidate.email", "email", c.Email) {
		switch a, err := mail.ParseAddress(c.Email); {
		case err != nil || a.Address != c.Email:
			rs = append(rs, reasonf(ReasonInvalid, "candidate.email", "email is not a valid address"))
		case reservedEmail(c.Email):
			rs = append(rs, reasonf(ReasonReserved, "candidate.email", "email "+c.Email+" is an example value on a reserved domain"))
		}
	}
	if !blank("candidate.phone", "phone", c.Phone) && isFictionalPhone(c.Phone) {
		rs = append(rs, reasonf(ReasonFictional, "candidate.phone", "phone "+c.Phone+" is a fictional example number"))
	}
	blank("candidate.address.city", "city", c.Address.City)
	blank("candidate.address.region", "region", c.Address.Region)
	blank("candidate.address.country", "country", c.Address.Country)
	for _, f := range []struct{ field, label, v string }{{"candidate.address.line1", "street address", c.Address.Line1}, {"candidate.address.postal_code", "postal code", c.Address.PostalCode}} {
		if strings.TrimSpace(f.v) == "" {
			rs = append(rs, reasonf(ReasonBlank, f.field, f.label+" is empty: enter it or set it to "+askMe+" to leave the field for yourself"))
		}
	}
	for _, f := range []struct{ field, label, v string }{{"candidate.full_name", "full name", c.FullName}, {"candidate.preferred_name", "preferred name", c.PreferredName},
		{"candidate.address.line1", "street address", c.Address.Line1}, {"candidate.address.city", "city", c.Address.City}, {"candidate.address.region", "region", c.Address.Region}} {
		if hasPlaceholder(f.v) {
			rs = append(rs, reasonf(ReasonPlaceholder, f.field, f.label+" holds a placeholder: "+strings.Join(placeholderTokens(f.v), ", ")))
		}
	}
	sortReasons(rs)
	return verdict(rs...)
}

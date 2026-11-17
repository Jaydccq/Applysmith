package onboarding

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

var visaClasses = []string{"H-1B", "H-1B1", "E-3", "E-2", "L-1", "O-1", "TN", "other"}

var workAuthFields = []Field{
	{Name: "authorization_status", Type: "string", Doc: "Your work authorization status; there is no default.", Enum: answerbank.AuthStatuses},
	{Name: "status_detail", Type: "string", Doc: "For a work visa the visa class; for a student work authorization its end date, YYYY-MM-DD.", Enum: visaClasses},
	{Name: "authorized_countries", Type: "string-list", Doc: "The countries where you can work: names or two-letter codes, for example United States or US."},
	{Name: "citizenships", Type: "string-list", Doc: "Every country you are a citizen of, both for a dual national: names or two-letter codes. Sanctions and export-control questions are answered from it."},
	{Name: "requires_sponsorship_now", Type: "bool", Doc: "Whether you need sponsorship now; never inferred from the status."},
	{Name: "requires_sponsorship_future", Type: "bool", Doc: "Whether you will need sponsorship in the future; a separate question."},
	{Name: "security_clearance", Type: "string", Doc: "Clearance you hold, or none."},
	{Name: "relocation_default", Type: "string", Doc: "Willingness to relocate.", Enum: []string{"open", "closed"}},
	{Name: "relocation_places", Type: "string-list", Doc: "Preferred places, in order."},
	{Name: "relocation_excluded", Type: "string-list", Doc: "Places you refuse."},
	{Name: "work_modes", Type: "string-list", Doc: "Any of remote, hybrid, onsite.", Enum: []string{"remote", "hybrid", "onsite"}},
}

type workAuthInput struct {
	Status      string   `json:"authorization_status"`
	Detail      string   `json:"status_detail"`
	Countries   []string `json:"authorized_countries"`
	Citizen     []string `json:"citizenships"`
	SponsorNow  *bool    `json:"requires_sponsorship_now"`
	SponsorNext *bool    `json:"requires_sponsorship_future"`
	Clearance   string   `json:"security_clearance"`
	RelocDef    string   `json:"relocation_default"`
	RelocPlaces []string `json:"relocation_places"`
	RelocExcl   []string `json:"relocation_excluded"`
	WorkModes   []string `json:"work_modes"`
}

func inList(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func applyWorkAuth(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in workAuthInput
	if err := decodeInput(raw, workAuthFields, &in); err != nil {
		return Applied{}, err
	}
	fe := FieldErrors{}
	var ops []workspace.Op
	if in.Status != "" {
		visa := in.Status
		switch {
		case !inList(in.Status, answerbank.AuthStatuses):
			fe.add("authorization_status", "must be one of "+strings.Join(answerbank.AuthStatuses, ", "))
		case in.Status == "work_visa":
			if !inList(in.Detail, visaClasses) {
				fe.add("status_detail", "choose the visa class from "+strings.Join(visaClasses, ", "))
			}
			visa += ": " + in.Detail
		case in.Status == "student_work_authorization":
			if _, err := time.Parse("2006-01-02", in.Detail); err != nil {
				fe.add("status_detail", "enter the authorization end date as YYYY-MM-DD")
			}
			visa += ": " + in.Detail
		case in.Detail != "":
			fe.add("status_detail", "applies only to work_visa and student_work_authorization")
		}
		ops = append(ops, setOp("work_authorization.visa_status", visa))
	}
	if in.Countries != nil {
		up := make([]string, len(in.Countries))
		for i, c := range in.Countries {
			code, ok := match.CountryCode(c)
			if strings.EqualFold(strings.TrimSpace(c), "EU") {
				code, ok = "EU", true // a region the answer bank reads for its member countries
			}
			if !ok {
				fe.add("authorized_countries", strings.TrimSpace(c)+" is not a country this app knows; enter its two-letter code, for example US")
			}
			up[i] = code
		}
		ops = append(ops, setOp("work_authorization.authorized_countries", up))
	}
	if in.Citizen != nil {
		up := make([]string, len(in.Citizen))
		for i, c := range in.Citizen {
			code, ok := match.CountryCode(c)
			if !ok {
				fe.add("citizenships", strings.TrimSpace(c)+" is not a country this app knows; enter its two-letter code, for example CN")
			}
			up[i] = code
		}
		ops = append(ops, setOp("work_authorization.citizenships", up))
	}
	if in.SponsorNow != nil {
		ops = append(ops, setOp("work_authorization.requires_sponsorship_now", *in.SponsorNow))
	}
	if in.SponsorNext != nil {
		ops = append(ops, setOp("work_authorization.requires_sponsorship_future", *in.SponsorNext))
	}
	if c := strings.TrimSpace(in.Clearance); c != "" {
		ops = append(ops, setOp("work_authorization.security_clearance", c))
	}
	if in.RelocDef != "" {
		ops = append(ops, setOp("preferences.relocation.default", in.RelocDef))
	}
	if in.RelocPlaces != nil {
		ops = append(ops, setOp("preferences.relocation.places", in.RelocPlaces))
	}
	if in.RelocExcl != nil {
		ops = append(ops, setOp("preferences.relocation.excluded", in.RelocExcl))
	}
	if in.WorkModes != nil {
		ops = append(ops, setOp("preferences.work_modes", in.WorkModes))
	}
	if len(fe) > 0 {
		return Applied{}, fe
	}
	changed, err := editProfile(env, ops, dry)
	if err != nil {
		return Applied{}, err
	}
	return Applied{Step: "work-authorization", DryRun: dry, Wrote: changed && !dry, Detection: Evaluate(ctx, env, mustStep("work-authorization")),
		Notes: []string{"Sponsorship now and in the future are separate answers; neither is inferred from the status."}}, nil
}

func detectWorkAuth(_ context.Context, env *Env) Detection {
	p, _, bad := profileData(env)
	if bad != nil {
		return *bad
	}
	wa := p.WorkAuthorization
	var rs []Reason
	unanswered := func(field, what string) {
		rs = append(rs, reasonf(ReasonBlank, field, what+" is unanswered"))
	}
	_, known := answerbank.AuthStatus(wa.VisaStatus)
	switch {
	case strings.TrimSpace(wa.VisaStatus) == "":
		unanswered("work_authorization.visa_status", "authorization status")
	case !known:
		rs = append(rs, reasonf(ReasonInvalid, "work_authorization.visa_status", "authorization status "+wa.VisaStatus+" names no status; choose citizen, permanent resident, work visa, student work authorization, other, not authorized or prefer not to say"))
	}
	if len(wa.AuthorizedCountries) == 0 {
		unanswered("work_authorization.authorized_countries", "countries where you can work")
	}
	if wa.RequiresSponsorshipNow == nil {
		unanswered("work_authorization.requires_sponsorship_now", "sponsorship needed now")
	}
	if wa.RequiresSponsorshipFuture == nil {
		unanswered("work_authorization.requires_sponsorship_future", "sponsorship needed in the future")
	}
	if strings.TrimSpace(wa.SecurityClearance) == "" {
		unanswered("work_authorization.security_clearance", "security clearance (enter none if you hold none)")
	}
	if p.Preferences.Relocation.Default == "" {
		unanswered("preferences.relocation.default", "willingness to relocate")
	}
	if len(p.Preferences.WorkModes) == 0 {
		unanswered("preferences.work_modes", "remote, hybrid or on-site preference")
	}
	return verdict(rs...)
}

// ---- step 5: targets ----

var targetsFields = []Field{
	{Name: "roles", Type: "object-list", Doc: "Target roles: title, seniority and priority (primary or secondary, default primary); replaces the stored list.", Required: true},
	{Name: "locations", Type: "string-list", Doc: "Target locations, or \"anywhere or remote\"; replaces the stored list."},
	{Name: "compensation", Type: "object", Doc: "currency (three letters), period (year or hour), minimum and target as positive integers with minimum not above target."},
	{Name: "batch_skip_companies", Type: "string-list", Doc: "Employers that batch applying leaves to you; supplying the list edits it (an empty list skips no one)."},
	{Name: "skip_list_confirmed", Type: "bool", Doc: "Confirm the batch skip list as it is now."},
}

type targetRole struct {
	Title     string `json:"title"`
	Seniority string `json:"seniority"`
	Priority  string `json:"priority,omitempty"`
}

type targetComp struct {
	Currency string `json:"currency"`
	Period   string `json:"period"`
	Minimum  *int   `json:"minimum"`
	Target   *int   `json:"target"`
}

type targetsInput struct {
	Roles     *[]targetRole `json:"roles"`
	Locations *[]string     `json:"locations"`
	Comp      *targetComp   `json:"compensation"`
	SkipList  *[]string     `json:"batch_skip_companies"`
	Confirmed bool          `json:"skip_list_confirmed"`
}

func skipListScope(p workspace.Profile) string {
	b, _ := json.Marshal(p.EffectiveSkipCompanies())
	return hashBytes(b)
}

func scopeTargets(_ context.Context, env *Env) (string, error) {
	p, _, bad := profileData(env)
	if bad != nil {
		return "", errNoDatabase
	}
	return skipListScope(p), nil
}

func applyTargets(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in targetsInput
	if err := decodeInput(raw, targetsFields, &in); err != nil {
		return Applied{}, err
	}
	fe := FieldErrors{}
	var ops []workspace.Op
	if in.Roles != nil {
		for i, r := range *in.Roles {
			if strings.TrimSpace(r.Title) == "" {
				fe.add("roles["+itoa(i)+"].title", "must not be blank")
			}
			if r.Seniority == "" {
				fe.add("roles["+itoa(i)+"].seniority", "is required")
			}
		}
		ops = append(ops, setOp("targets.roles", *in.Roles))
	}
	if in.Locations != nil {
		ops = append(ops, setOp("targets.locations", *in.Locations))
	}
	if c := in.Comp; c != nil {
		if c.Currency != "" {
			ops = append(ops, setOp("targets.compensation.currency", strings.ToUpper(c.Currency)))
		}
		if c.Period != "" {
			ops = append(ops, setOp("targets.compensation.period", c.Period))
		}
		for _, f := range []struct {
			name string
			v    *int
		}{{"minimum", c.Minimum}, {"target", c.Target}} {
			if f.v == nil {
				continue
			}
			if *f.v <= 0 {
				fe.add("compensation."+f.name, "must be a positive integer")
				continue
			}
			ops = append(ops, setOp("targets.compensation."+f.name, *f.v))
		}
	}
	if in.SkipList != nil {
		ops = append(ops, setOp("batch_skip_companies", *in.SkipList))
	}
	if len(fe) > 0 {
		return Applied{}, fe
	}
	changed, err := editProfile(env, ops, dry)
	if bad, ok := err.(FieldErrors); ok {
		// The profile names a refused value by its path in the file
		// (targets.compensation.minimum); the step names it by its own field.
		err = FieldErrors{}
		for k, m := range bad {
			err.(FieldErrors).add(strings.TrimPrefix(k, "targets."), m)
		}
	}
	if err != nil {
		return Applied{}, err
	}
	if in.Confirmed && !dry {
		scope, err := scopeTargets(ctx, env)
		if err == nil {
			err = env.setDecision(ctx, reviewKey("targets"), scope)
		}
		if err != nil {
			return Applied{}, &EnvError{Message: "the skip-list confirmation could not be stored", Err: err}
		}
	}
	return Applied{Step: "targets", DryRun: dry, Wrote: changed && !dry, Detection: Evaluate(ctx, env, mustStep("targets"))}, nil
}

func detectTargets(_ context.Context, env *Env) Detection {
	p, raw, bad := profileData(env)
	if bad != nil {
		return *bad
	}
	t := p.Targets
	var rs []Reason
	need := func(field, text string) { rs = append(rs, reasonf(ReasonBlank, field, text)) }
	if len(t.Roles) == 0 {
		need("targets.roles", "no target role is listed")
	}
	for i, r := range t.Roles {
		if strings.TrimSpace(r.Title) == "" {
			need("targets.roles["+itoa(i)+"].title", "role "+itoa(i+1)+" has no title")
		}
		if r.Seniority == "" {
			need("targets.roles["+itoa(i)+"].seniority", "role "+itoa(i+1)+" has no seniority")
		}
	}
	if len(t.Locations) == 0 {
		need("targets.locations", "no target location is listed (enter \"anywhere or remote\" if any place works)")
	}
	c := t.Compensation
	if c.Currency == "" {
		need("targets.compensation.currency", "currency is empty")
	}
	if c.Period == "" {
		need("targets.compensation.period", "pay period is empty")
	}
	if c.Minimum == nil {
		need("targets.compensation.minimum", "compensation minimum is empty")
	}
	if c.Target == nil {
		need("targets.compensation.target", "compensation target is empty")
	}
	var doc map[string]any
	if _ = yaml.Unmarshal(raw, &doc); doc != nil {
		if _, present := doc["batch_skip_companies"]; !present {
			rs = append(rs, reasonf(ReasonReview, "batch_skip_companies",
				"the batch skip list ("+strings.Join(p.EffectiveSkipCompanies(), ", ")+") needs review: confirm it or edit it"))
		}
	}
	return verdict(rs...)
}

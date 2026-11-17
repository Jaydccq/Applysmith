package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/answerbank"
	"applysmith/internal/cvparse"
	"applysmith/internal/llm"
	"applysmith/internal/workspace"
)

// questions asks what a step needs and returns the writer's input.
func (f *Flow) questions(ctx context.Context, s Step) (json.RawMessage, error) {
	var in map[string]any
	var err error
	switch s.ID {
	case "prerequisites":
		in = map[string]any{}
	case "workspace":
		in, err = f.askWorkspace()
	case "identity":
		in, err = f.askIdentity()
	case "cv":
		in, err = f.askCV(ctx)
	case "work-authorization":
		in, err = f.askWorkAuth()
	case "targets":
		in, err = f.askTargets()
	case "llm":
		in, err = f.askLLM(ctx)
	case "story":
		in, err = f.askStory(ctx)
	case "resumes":
		in, err = f.askResumes()
	case "answer-bank":
		in, err = f.askAnswerBank(ctx)
	case "browser", "orientation":
		in = map[string]any{}
	case "discovery":
		in, err = f.askDiscovery()
	default:
		err = &RejectedError{Message: "step " + s.ID + " has no interactive questions"}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(in)
}

func (f *Flow) askWorkspace() (map[string]any, error) {
	cur := ""
	if f.Env.Workspace != nil {
		cur = f.Env.Workspace.Root
	}
	a, err := f.ask("Workspace location", cur)
	if err != nil {
		return nil, err
	}
	if a == cur {
		return map[string]any{}, nil
	}
	return map[string]any{"path": a}, nil
}

// askChanged asks for a field and records it only when it differs from the
// stored value, so repeating a run with the same answers writes nothing. An
// empty stored value offers offer instead; a stored one is never replaced.
func (f *Flow) askChanged(in map[string]any, key, label, stored, offer string) error {
	def := stored
	if strings.TrimSpace(stored) == "" {
		def = offer
	}
	a, err := f.ask(label, def)
	if err != nil {
		return err
	}
	if a != "" && a != stored {
		in[key] = a
	}
	return nil
}

// askIdentity offers, for a field the profile leaves empty, what cv.md says
// (answerbank.IdentityFromCV, as the dashboard does).
func (f *Flow) askIdentity() (map[string]any, error) {
	p, _, _ := profileData(f.Env)
	c := p.Candidate
	in := map[string]any{}
	fromCV := map[string]string{}
	if cv, err := f.Env.Workspace.Read(workspace.FileCV); err == nil {
		fromCV = answerbank.IdentityFromCV(string(cv.Data))
	}
	if len(fromCV) > 0 {
		fmt.Fprintln(f.Out, "  An empty field offers what cv.md says; Enter takes it.")
	}
	for _, q := range []struct{ key, label, stored string }{
		{"full_name", "Full name (typed verbatim into application forms)", c.FullName},
		{"email", "Email", c.Email},
		{"phone", "Phone (international form, +...)", c.Phone},
		{"city", "City", c.Address.City},
		{"region", "State or region", c.Address.Region},
		{"country", "Country (two-letter code, for example GB)", c.Address.Country},
		{"street_address", "Street address, or " + askMe + " to leave it to yourself", c.Address.Line1},
		{"postal_code", "Postal code, or " + askMe, c.Address.PostalCode},
	} {
		if err := f.askChanged(in, q.key, q.label, q.stored, fromCV[q.key]); err != nil {
			return nil, err
		}
	}
	more, err := f.yesNo("Add optional details (preferred name, county, date of birth, links)?")
	if err != nil || !more {
		return in, err
	}
	for _, q := range []struct{ key, label, stored string }{
		{"preferred_name", "Preferred name", c.PreferredName},
		{"county", "County", c.Address.County},
		{"date_of_birth", "Date of birth (YYYY-MM-DD; used only to answer age questions)", c.DateOfBirth},
		{"linkedin", "LinkedIn URL", p.Links.LinkedIn},
		{"github", "GitHub URL", p.Links.GitHub},
		{"portfolio", "Portfolio URL", p.Links.Portfolio},
	} {
		if err := f.askChanged(in, q.key, q.label, q.stored, fromCV[q.key]); err != nil {
			return nil, err
		}
	}
	return in, nil
}

func (f *Flow) askCV(ctx context.Context) (map[string]any, error) {
	in := map[string]any{}
	path, err := f.ask("Path to your CV file (.pdf, .docx, .md or .txt); empty keeps the stored cv.md", "")
	if err != nil {
		return nil, err
	}
	text := ""
	if path != "" {
		if text, err = ImportCVFile(ctx, path); err != nil {
			return nil, err
		}
		fmt.Fprintf(f.Out, "\nCV text to review:\n%s\n", text)
		confirmed, err := f.yesNo("Use this text as your CV?")
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, &RejectedError{Message: "CV import was not confirmed; no files were changed"}
		}
		in["markdown"] = text
	} else if c, err := f.Env.Workspace.Read(workspace.FileCV); err == nil {
		text = string(c.Data)
	}
	draft := cvparse.Parse(text)
	if draft.Entries() == 0 {
		return in, nil
	}
	fmt.Fprintf(f.Out, "  The CV parser found %s:\n", draft.Summary())
	for _, j := range draft.Employment {
		fmt.Fprintf(f.Out, "    job: %s, %s (%s to %s)\n", j.Title, j.Employer, shown(j.Start), shown(j.End))
	}
	for _, e := range draft.Education {
		fmt.Fprintf(f.Out, "    study: %s, %s (%s to %s)\n", e.Degree, e.School, shown(e.Start), shown(e.End))
	}
	if len(draft.Skills) > 0 {
		fmt.Fprintf(f.Out, "    skills: %s\n", strings.Join(draft.Skills, ", "))
	}
	yes, err := f.yesNo("Store these entries in your profile?")
	if err != nil {
		return nil, err
	}
	if !yes {
		in["review_complete"] = true
		return in, nil
	}
	jobs := make([]cvJob, len(draft.Employment))
	for i, j := range draft.Employment {
		jobs[i] = cvJob{Employer: j.Employer, Title: j.Title}
		for _, d := range []struct {
			label, from string
			to          *string
		}{{"start", j.Start, &jobs[i].Start}, {"end", j.End, &jobs[i].End}} {
			if *d.to, err = f.confirmMonth(j.Employer+" "+d.label, d.from); err != nil {
				return nil, err
			}
		}
	}
	studies := make([]cvStudy, len(draft.Education))
	for i, e := range draft.Education {
		studies[i] = cvStudy{School: e.School, Degree: e.Degree, Field: e.Field}
		for _, d := range []struct {
			label, from string
			to          *string
		}{{"start", e.Start, &studies[i].Start}, {"end", e.End, &studies[i].End}} {
			if *d.to, err = f.confirmMonth(e.School+" "+d.label, d.from); err != nil {
				return nil, err
			}
		}
	}
	skills := make([]cvSkill, len(draft.Skills))
	for i, k := range draft.Skills {
		skills[i] = cvSkill{Name: k}
	}
	in["employment"], in["education"], in["skills"] = jobs, studies, skills
	return in, nil
}

// shown is a draft date as printed: a year without a month says so.
func shown(date string) string {
	if cvparse.YearOnly(date) {
		return date + " (month not stated)"
	}
	return date
}

// confirmMonth returns a draft date ready for the profile. A date the CV gave
// as a bare year is completed only with a month the user types; an empty
// answer leaves the date blank rather than guessing one.
func (f *Flow) confirmMonth(what, date string) (string, error) {
	if !cvparse.YearOnly(date) {
		return date, nil
	}
	for i := 0; i < maxAttempts; i++ {
		a, err := f.ask(fmt.Sprintf("Month of %s in %s (1-12; empty leaves the date blank)", what, date), "")
		if err != nil {
			return "", err
		}
		if a == "" {
			return "", nil
		}
		if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= 12 {
			return fmt.Sprintf("%s-%02d", date, n), nil
		}
		fmt.Fprintln(f.Out, "  answer a month number from 1 to 12")
	}
	return "", FieldErrors{"month": "no valid month was given for " + what}
}

func (f *Flow) askWorkAuth() (map[string]any, error) {
	in := map[string]any{}
	status, err := f.ask("Work authorization ("+strings.Join(answerbank.AuthStatuses, ", ")+")", "")
	if err != nil {
		return nil, err
	}
	in["authorization_status"] = status
	switch status {
	case "work_visa":
		if in["status_detail"], err = f.ask("Visa class ("+strings.Join(visaClasses, ", ")+")", ""); err != nil {
			return nil, err
		}
	case "student_work_authorization":
		if in["status_detail"], err = f.ask("Authorization end date (YYYY-MM-DD)", ""); err != nil {
			return nil, err
		}
	}
	countries, err := f.ask("Countries where you can work (two-letter codes, comma-separated)", "")
	if err != nil {
		return nil, err
	}
	in["authorized_countries"] = splitList(countries)
	if in["requires_sponsorship_now"], err = f.yesNo("Do you need visa sponsorship now?"); err != nil {
		return nil, err
	}
	if in["requires_sponsorship_future"], err = f.yesNo("Will you need visa sponsorship in the future?"); err != nil {
		return nil, err
	}
	if in["security_clearance"], err = f.ask("Security clearance you hold (none if you hold none)", ""); err != nil {
		return nil, err
	}
	if in["relocation_default"], err = f.ask("Willing to relocate? (open, closed)", ""); err != nil {
		return nil, err
	}
	for _, q := range []struct{ key, label string }{
		{"relocation_places", "Preferred places to relocate to, in order (comma-separated, may be empty)"},
		{"relocation_excluded", "Places you refuse (comma-separated, may be empty)"}} {
		a, err := f.ask(q.label, "")
		if err != nil {
			return nil, err
		}
		in[q.key] = splitList(a)
	}
	modes, err := f.ask("Work modes (any of remote, hybrid, onsite; comma-separated)", "")
	if err != nil {
		return nil, err
	}
	in["work_modes"] = splitList(modes)
	return in, nil
}

func (f *Flow) askTargets() (map[string]any, error) {
	in := map[string]any{}
	var roles []targetRole
	for {
		title, err := f.ask("Target role title", "")
		if err != nil {
			return nil, err
		}
		sen, err := f.ask("Seniority (intern, junior, mid, senior, lead, staff, principal, manager, director, vp, executive)", "")
		if err != nil {
			return nil, err
		}
		pri, err := f.ask("Priority (primary, secondary)", "primary")
		if err != nil {
			return nil, err
		}
		roles = append(roles, targetRole{Title: title, Seniority: sen, Priority: pri})
		more, err := f.yesNo("Add another role?")
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	in["roles"] = roles
	locs, err := f.ask("Locations (comma-separated, or \"anywhere or remote\")", "")
	if err != nil {
		return nil, err
	}
	in["locations"] = splitList(locs)
	comp := map[string]any{}
	if comp["currency"], err = f.ask("Compensation currency (three letters, for example USD)", ""); err != nil {
		return nil, err
	}
	if comp["period"], err = f.ask("Pay period (year or hour)", ""); err != nil {
		return nil, err
	}
	for _, k := range []string{"minimum", "target"} {
		a, err := f.ask("Compensation "+k+" (a positive whole number)", "")
		if err != nil {
			return nil, err
		}
		n, convErr := strconv.Atoi(a)
		if convErr != nil {
			return nil, FieldErrors{"compensation." + k: "must be a positive whole number"}
		}
		comp[k] = n
	}
	in["compensation"] = comp
	p, _, _ := profileData(f.Env)
	fmt.Fprintf(f.Out, "  Batch applying leaves these employers to you: %s\n", strings.Join(p.EffectiveSkipCompanies(), ", "))
	keep, err := f.yesNo("Keep this batch skip list as it is?")
	if err != nil {
		return nil, err
	}
	if keep {
		in["skip_list_confirmed"] = true
		return in, nil
	}
	a, err := f.ask("Employers to skip (comma-separated; empty skips no one)", "")
	if err != nil {
		return nil, err
	}
	in["batch_skip_companies"] = splitList(a)
	return in, nil
}

func (f *Flow) askLLM(ctx context.Context) (map[string]any, error) {
	names := slices.Concat(apiPresets, cliPresets, []string{"offline"})
	choice, err := f.ask("Model provider ("+strings.Join(names, ", ")+"; offline means no key yet)", "")
	if err != nil {
		return nil, err
	}
	switch {
	case choice == "offline":
		return map[string]any{"offline": true}, nil
	case slices.Contains(cliPresets, choice):
		fmt.Fprintln(f.Out, "  "+CLILimits)
		yes, err := f.yesNo("Continue with the " + choice + " command-line tool?")
		if err != nil || !yes {
			if err == nil {
				err = &RejectedError{Message: "the command-line preset was not saved"}
			}
			return nil, err
		}
		return map[string]any{"cli": choice, "acknowledge_cli_limits": true}, nil
	}
	p, ok := llm.PresetByName(choice)
	if !ok || !p.InSetup {
		return nil, FieldErrors{"preset": "setup offers " + strings.Join(names, ", ")}
	}
	// The ChatGPT plan takes no key; applying it says how to sign in when the
	// plan is not signed in yet.
	if p.Kind == llm.KindChatGPT {
		return map[string]any{"preset": p.Name}, nil
	}
	if f.Env.Vault != nil {
		if _, err := f.Env.Vault.Test(llm.KeyName(p.Name)); err == nil {
			fmt.Fprintf(f.Out, "  A key for %s is already stored; it is not shown.\n", p.Label)
			return map[string]any{"preset": p.Name}, nil
		}
	}
	key, err := f.In.Secret(fmt.Sprintf("%s API key (create one at %s)", p.Label, p.KeyURL))
	if err != nil {
		return nil, err
	}
	if err := SaveProviderKey(ctx, f.Env, p.Name, key); err != nil {
		return nil, err
	}
	return map[string]any{"preset": p.Name}, nil
}

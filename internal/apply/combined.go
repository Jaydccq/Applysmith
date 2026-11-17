package apply

import (
	"context"
	"strings"
)

// combinedAccount is the run's hand-over from the sign-up lane to the
// application walk: the create-account fields sit inside the application, so
// the walk registers on them. A run creates at most one account this way.
type combinedAccount struct {
	host, name, system, tenant string
	// typed is set once the account fields were filled; done once the press
	// that sent the page was settled.
	typed, done bool
	// created is true when the password was generated for this attempt and so
	// may be removed again.
	created bool
	email   string
}

// readCombined reports whether a page is both a registration and an
// application: a password and an email box beside fields of the application
// (any other box, a résumé upload).
func readCombined(st *PageState) *signupForm {
	f := &signupForm{maxLen: policyMaxLen(st.Text)}
	other := 0
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		switch {
		case in.Type == "password":
			f.passwords = append(f.passwords, in)
			if in.MaxLength > 0 && (f.maxLen == 0 || in.MaxLength < f.maxLen) {
				f.maxLen = in.MaxLength
			}
		case in.Type == "checkbox" || in.Type == "radio":
		case in.Type == "email" || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete), "mail"):
			f.emails = append(f.emails, in)
		default:
			other++
		}
	}
	if len(f.passwords) == 0 || len(f.emails) == 0 || other == 0 {
		return nil
	}
	return f
}

// newAcct builds the account helper the walk's hook types with.
func (s *session) newAcct(host string) *acct {
	e := s.e
	idx := e.signinIndex(host)
	return &acct{r: s.r, e: e, s: s, host: host, system: e.Run.ATS, tenant: e.Run.Tenant, tm: s.r.d.Page.Account.withDefaults(), rec: &e.Run.Signins[idx]}
}

// combinedType fills the account fields of a combined page: only when the page
// is on the host the account stage classified, reads as both a registration
// and an application, and no captcha stands on it. Otherwise nothing is typed
// and the page is left to the user.
func (s *session) combinedType(ctx context.Context, st *PageState) *stop {
	c := s.e.combined
	if c == nil || c.typed || c.done {
		return nil
	}
	host, _ := parseHost(st.URL)
	form := readCombined(st)
	switch {
	case host != c.host:
		return nil // another host's page is not the one the stage classified
	case form == nil:
		return nil
	case DetectChallenge(st) != nil:
		s.e.Run.Warnings = append(s.e.Run.Warnings, "a captcha stands on the combined account form; nothing was typed and the page is left to you")
		c.done = true
		return nil
	}
	a := s.newAcct(host)
	creds := s.r.d.Page.Credentials
	if creds == nil {
		return nil
	}
	email := a.accountEmail()
	if email == "" {
		return nil
	}
	pw, created, err := creds.GenerateFor(c.name, form.maxLen)
	if err != nil {
		return a.fail(ClassEnvironment, "", "a password could not be prepared: %v", err)
	}
	c.created, c.email = created, email
	// refuse removes a password this attempt created: the form was not filled,
	// so it is never sent as a registration.
	refuse := func(stp *stop) *stop {
		if created {
			_ = creds.Remove(context.WithoutCancel(ctx), c.name)
		}
		return stp
	}
	for _, in := range form.emails {
		if stp := a.typeInto(ctx, in, email); stp != nil {
			return refuse(stp)
		}
	}
	for _, in := range form.passwords {
		if stp := a.typeSecret(ctx, in, pw); stp != nil {
			return refuse(stp)
		}
	}
	c.typed = true
	return nil
}

// combinedSettled runs after the control that sends a combined page was
// pressed: the password stays saved as the host's site override only if the
// combined form is gone, and then the account counts as created (the sign-up
// quota counts it and the credential index records it); otherwise a password
// this attempt generated is removed.
func (s *session) combinedSettled(ctx context.Context) {
	c := s.e.combined
	if c == nil || !c.typed || c.done {
		return
	}
	c.done = true
	creds := s.r.d.Page.Credentials
	st, err := Probe(ctx, s.tab)
	if err != nil || readCombined(st) != nil {
		if c.created && creds != nil {
			_ = creds.Remove(context.WithoutCancel(ctx), c.name)
		}
		return
	}
	a := s.newAcct(c.host)
	if creds != nil {
		_ = creds.Created(context.WithoutCancel(ctx), c.host, c.system, c.tenant, c.email, c.name)
	}
	if err := a.recordAttempt(ctx); err != nil {
		s.e.Run.Warnings = append(s.e.Run.Warnings, "the combined account could not be counted against the quota: "+err.Error())
	}
	a.rec.Succeeded = "created-account"
	a.rec.Tried = append(a.rec.Tried, "created-account")
}

// combinedCreate is the create-account control of the combined form whose
// account fields the walk typed, when the form ends in it instead of a Next or
// a Submit (SuccessFactors' "Create Account"): the walk presses it as the
// account stage would, once.
func (s *session) combinedCreate(st *PageState) *Control {
	c := s.e.combined
	if c == nil || !c.typed || c.done || readCombined(st) == nil {
		return nil
	}
	for i := range st.Controls {
		ctl := &st.Controls[i]
		if ctl.Chat || ctl.Chrome || ctl.Disabled {
			continue
		}
		for _, l := range ctl.Labels {
			if signupBtnRE.MatchString(strings.TrimSpace(l)) && !ssoRE.MatchString(l) {
				return ctl
			}
		}
	}
	return nil
}

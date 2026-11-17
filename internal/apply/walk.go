package apply

import (
	"applysmith/internal/discovery"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// MaxPages is the most pages one walk covers, whatever a caller asks.
const MaxPages = 10

// maxOpens is how often the walk opens the form from an Apply control.
const maxOpens = 2

// maxConsents is how many consent gates (the cookie notice, then the privacy
// dialog the Apply press opens) one walk closes.
const maxConsents = 2

// WalkEnd is how a walk ended.
type WalkEnd string

const (
	EndSubmit           WalkEnd = "submit"
	EndNoNavigation     WalkEnd = "no-navigation"
	EndNoForm           WalkEnd = "no-form"
	EndPostingClosed    WalkEnd = "posting-closed"
	EndAlreadyApplied   WalkEnd = "already-applied"
	EndEmailApplication WalkEnd = "email-application"
	EndLoginRequired    WalkEnd = "login-required"
	EndBotChallenge     WalkEnd = "bot-challenge"
	EndOneClickApply    WalkEnd = "one-click-apply"
	EndMaxPages         WalkEnd = "max-pages"
	EndStalled          WalkEnd = "stalled"
	EndSubmitDisabled   WalkEnd = "submit-disabled"
	// EndConsentGate is a page that asks for consent to process the person's
	// data before the form (Jobvite's "Data Consent"): the person's to give.
	EndConsentGate WalkEnd = "consent-gate"
)

// Class is the failure class of a walk end that is not Submit. `no-form` is
// posting unless the page still offers an Apply control, in which case it is a
// defect; login-required is credential; posting-closed, already-applied and
// email-application are posting; a bot check left to the person is
// verification; every other end is a defect.
func (e WalkEnd) Class(stillOffersApply bool) Class {
	switch e {
	case EndNoForm:
		if stillOffersApply {
			return ClassDefect
		}
		return ClassPosting
	case EndLoginRequired:
		return ClassCredential
	case EndBotChallenge:
		return ClassVerification
	case EndConsentGate:
		return ClassAnswerGap
	case EndPostingClosed, EndAlreadyApplied, EndEmailApplication:
		return ClassPosting
	}
	return ClassDefect
}

// WalkTiming holds the walk's clocks; a zero field takes the specified value.
type WalkTiming struct {
	// AfterForm is how long a step that follows a form page is re-read for a
	// form; First is the single look at a first page.
	AfterForm, First, Poll time.Duration
	// PressWait is how long the walk waits for a pressed control to move the
	// page.
	PressWait time.Duration
	// OpenWait is how long a LinkedIn posting is given to show its Easy Apply
	// modal, TerminalWait how long its text is polled for a closed or applied
	// notice, ConfirmWait how long a LinkedIn press is given to confirm.
	OpenWait, TerminalWait, ConfirmWait time.Duration
	// Saving is how long a Workday step behind its loading page is waited for:
	// a save that carries an upload can take longer than a look.
	Saving time.Duration
}

func (t WalkTiming) withDefaults() WalkTiming {
	if t.AfterForm == 0 {
		t.AfterForm = 20 * time.Second
	}
	if t.First == 0 {
		t.First = 5 * time.Second
	}
	if t.Poll == 0 {
		t.Poll = time.Second
	}
	if t.PressWait == 0 {
		t.PressWait = 8 * time.Second
	}
	if t.OpenWait == 0 {
		t.OpenWait = 10 * time.Second
	}
	if t.TerminalWait == 0 {
		t.TerminalWait = 20 * time.Second
	}
	if t.ConfirmWait == 0 {
		t.ConfirmWait = 30 * time.Second
	}
	if t.Saving == 0 {
		t.Saving = 90 * time.Second
	}
	return t
}

// walkResult is what the walk hands to the stages after it.
type walkResult struct {
	End    WalkEnd
	Detail string
	// Offers is true when the final page still offers an Apply control.
	Offers bool
	// Last is the last page state, for the submit stage.
	Last *PageState
	// NoAccount is set when the sign-in the walk ended on belongs to a job
	// board: the chain never creates an account there.
	NoAccount bool
}

type walker struct {
	r     *Runner
	e     *Exec
	s     *session
	tm    WalkTiming
	pages int
	opens int
	// codePanes counts code panes completed on arrival; at most 2 per walk.
	codePanes int
	// codeEntries counts the code-entry dialogs completed; see maxCodeEntries.
	codeEntries int
	opened      map[string]bool
	// reopened marks the one press of an opener already followed, after a code
	// sign-in (ADP).
	reopened bool
	// reloaded marks the one reload of a page DataDome blocked.
	reloaded bool
	seen     map[string]int
	// decided marks the pages the page-decision model was already asked about.
	decided map[string]bool
	// icimsDone marks iCIMS's email step as submitted: once per walk.
	icimsDone bool
	// adpAuthDone marks ADP's email sign-in step as submitted: once per walk.
	adpAuthDone, adpMethodDone bool
	// revalidated marks the pages a disabled Submit was re-validated on.
	revalidated map[string]bool
	// consents counts the consent gates closed; see maxConsents.
	consents int
	// coveredBy is what stood over the control the last press meant to
	// click, empty when that press reached its control.
	coveredBy string
	// residencePicked and dataAccepted: one of each per walk.
	residencePicked, dataAccepted bool
	// policyAgreed: one employer-policy Agree per walk.
	policyAgreed bool
}

// walk runs the page walk: look, fill, check and fix each page, and press only
// navigation through gates.PressControl, until the form is ready to submit or
// the walk ends for a stated reason.
func (r *Runner) walk(ctx context.Context, e *Exec, s *session) (*walkResult, *stop) {
	w := &walker{r: r, e: e, s: s, tm: r.d.Page.Timing.withDefaults(), opened: map[string]bool{}, seen: map[string]int{}, decided: map[string]bool{}, revalidated: map[string]bool{}}
	return w.run(ctx)
}

func (w *walker) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

var dataConsentRE = lazyregexp.New(`(?i)^\s*data\s+(?:processing\s+)?consent\s*$`)

// dataConsentPage is the heading of a data-consent page: nothing to type and
// nothing the walk presses.
func dataConsentPage(st *PageState, afterForm bool) string {
	for _, in := range st.Inputs {
		if in.Kind != "select" && in.Kind != "checkbox" && !in.Furniture {
			return ""
		}
	}
	if c := Categorize(st, afterForm); len(c.Nav)+len(c.Submit)+len(c.Openers)+len(c.InFormApply) > 0 {
		return ""
	}
	for _, l := range st.Lines {
		if dataConsentRE.MatchString(l) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

var dataAcceptRE = lazyregexp.New(`(?i)^\s*(?:i\s+)?(?:accept|agree|consent|acknowledge)(?:\s+and\s+continue)?\s*$`)

// dataConsent answers a data-consent page when the consent-control gate allows
// it (the legal setting): the residence from the profile, then the page's
// accept. It reports whether it acted, else why the page stays the person's.
func (w *walker) dataConsent(ctx context.Context, st *PageState, heading string) (bool, string, *stop) {
	on := w.r.d.Page.Settings != nil && w.r.d.Page.Settings().AutoAcceptLegalDeclarations
	d := gates.ConsentControl(gates.ConsentInput{Label: heading, PageMarksRequired: true, SettingOn: on, Surface: gates.SurfaceForm})
	if err := w.r.audit(w.e, d, st.URL, false); err != nil {
		return false, "", &stop{class: ClassEnvironment, reason: err.Error()}
	}
	if !d.Allowed() {
		return false, d.Reason, nil
	}
	for _, c := range st.Controls {
		if c.Disabled || len(c.Labels) == 0 || !dataAcceptRE.MatchString(c.Labels[0]) {
			continue
		}
		if w.dataAccepted {
			return false, "the page stayed after its accept was pressed", nil
		}
		w.dataAccepted = true
		if _, s := w.pressAs(ctx, st, c, gates.StageDataConsent); s != nil {
			return false, "", s
		}
		w.s.walkNotes = append(w.s.walkNotes, fmt.Sprintf("data consent: %q pressed (%s)", c.Labels[0], d.Reason))
		return true, "", nil
	}
	for _, in := range st.Inputs {
		if in.Tag != "select" || in.Filled || w.residencePicked {
			continue
		}
		_, country := w.s.icimsApplicant(ctx)
		pick := match.ResidenceOption(in.Options, country)
		if pick < 0 {
			return false, fmt.Sprintf("no option of %q is the profile's country (%q)", clip(in.Label, 60), country), nil
		}
		w.residencePicked = true
		if ok, err := ChooseOption(ctx, w.s.tab, in.I, pick); err != nil || !ok {
			return false, "the residence menu did not take the choice", nil
		}
		w.s.walkNotes = append(w.s.walkNotes, fmt.Sprintf("data consent: residence %q for the profile's %q", in.Options[pick], country))
		return true, "", nil
	}
	return false, "no accept control the run may press", nil
}

var workdayFlowRE = lazyregexp.New(`(?i)/apply(?:/|$|\?)`)

// look probes the page. After a page that held a form, a step with no form is
// re-read every second for up to 20 seconds and the poll ends as soon as a form
// reads; a first page gets a single 5-second look.
func (w *walker) look(ctx context.Context, afterForm bool) (*PageState, *stop) {
	budget := w.tm.First
	if afterForm {
		budget = w.tm.AfterForm
	}
	start := time.Now()
	deadline := start.Add(budget)
	for {
		st, err := Probe(ctx, w.s.tab)
		if err != nil {
			return nil, w.browserStop(err)
		}
		// A cold Workday apply flow can draw only its header for longer than a
		// first look; it gets the long look.
		// So can a page that drew its text but not one control yet (ADP's job
		// page hydrates its buttons seconds later).
		if budget < w.tm.AfterForm && (len(st.Controls) == 0 || w.e.Run.ATS == ATSWorkday && workdayFlowRE.MatchString(st.URL)) {
			budget = w.tm.AfterForm
			deadline = start.Add(budget)
		}
		// A step still behind Workday's loading page is saving: the fields on
		// screen are the step going away, and filling them again re-sends what
		// is being saved. It is waited for, not read.
		if st.FlowLoading && budget < w.tm.Saving {
			budget = w.tm.Saving
			deadline = start.Add(budget)
		}
		if w.settled(st) || !time.Now().Before(deadline) {
			w.noteHost(st.URL)
			return st, nil
		}
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill)
		}
	}
}

// settled reports whether a look can stop waiting: a form reads, or the page
// says something final, or (for a first look) it offers a control to press.
func (w *walker) settled(st *PageState) bool {
	if st.FlowLoading {
		return siteErrorPage(st) // the step going away is waited for, the site's error page in it is not
	}
	if st.HasForm() || findWall(st) != nil {
		return true // a sign-in wall is the account stage's to clear, at once
	}
	if st.Loading {
		return false
	}
	if BotInterstitial(st.Title, st.Lines) != "" || ClosedNotice(st.Lines) != "" || AlreadyAppliedNotice(st.Lines) != "" || EmailOnlyNotice(st.Lines) != "" {
		return true
	}
	c := Categorize(st, w.pages > 0)
	if w.pages == 0 {
		return len(c.Openers) > 0 || len(c.Nav) > 0 || len(c.Submit) > 0 || c.OneClick != ""
	}
	// A review step after a form page has no field but does have its control.
	return len(c.Submit) > 0 || len(c.Nav) > 0
}

// readLead keeps the walk's first page without a form as the posting's
// overview, read again while its text grows: Workable draws the description
// after the Apply control the first look stops at.
func (w *walker) readLead(ctx context.Context, st *PageState) {
	deadline := time.Now().Add(w.tm.First)
	for time.Now().Before(deadline) && w.sleep(ctx, w.tm.Poll) {
		again, err := Probe(ctx, w.s.tab)
		if err != nil || again.URL != st.URL || len(again.Text) <= len(st.Text) {
			break
		}
		st = again
	}
	w.s.lead = st
}

func (w *walker) browserStop(err error) *stop {
	if w.e.Ctx.Err() != nil {
		return cancelledStop(StageFill)
	}
	return &stop{class: ClassEnvironment, reason: "the browser failed during the walk: " + err.Error()}
}

func (w *walker) run(ctx context.Context) (*walkResult, *stop) {
	afterForm := false
	for iter := 0; iter < 4*MaxPages+8; iter++ {
		if ctx.Err() != nil {
			return nil, cancelledStop(StageFill)
		}
		st, s := w.look(ctx, afterForm)
		if s != nil {
			return nil, s
		}
		w.s.last = st
		if w.s.pageCompany == "" {
			w.s.pageCompany = PageCompany(ctx, w.s.tab)
		}
		end := func(e WalkEnd, detail string) (*walkResult, *stop) {
			cats := Categorize(st, w.pages > 0)
			return &walkResult{End: e, Detail: detail, Offers: len(cats.Openers) > 0, Last: st}, nil
		}
		if res, s, acted := w.icimsStep(ctx, st); res != nil || s != nil {
			return res, s
		} else if acted {
			afterForm = false
			continue
		}
		if res, s, acted := w.adpAuthStep(ctx, st); res != nil || s != nil {
			return res, s
		} else if acted {
			afterForm = false
			continue
		}
		if res, s, acted := w.adpMethodStep(ctx, st); res != nil || s != nil {
			return res, s
		} else if acted {
			afterForm = false
			continue
		}
		if acted, s := w.codeDialogs(ctx, st); s != nil {
			return nil, s
		} else if acted {
			afterForm = false
			continue
		}
		if acted, title, why, s := w.pageDialogs(ctx, st); s != nil {
			return nil, s
		} else if acted {
			afterForm = false
			continue
		} else if why != "" {
			return end(EndConsentGate, fmt.Sprintf("the page asks you to agree to %q; it is yours to give: %s", title, why))
		}
		hasForm := st.HasForm()
		if !hasForm && w.s.lead == nil {
			w.readLead(ctx, st)
		}
		if hasForm && w.codePanes < 2 {
			if done, s := w.completeCodePane(ctx, st); s != nil {
				return nil, s
			} else if done {
				w.codePanes++
				continue
			}
		}
		if w.pages == 0 && w.opens == 0 && accountPageFirst(st) {
			// An account page is a gate, never "a different role": the chain
			// re-enters the account stage at this address, once.
			return end(EndLoginRequired, "the first page is an account page ("+redactURL(st.URL)+")")
		}
		if w.pages == 0 && w.opens == 0 && discovery.LostJob(w.e.Run.Input.URL, st.URL) {
			// The posting address was sent to a page that no longer names the
			// job; no model is asked what the page is, and a careers page's
			// job-search filters are never read as the application's form.
			note := "the posting address now opens " + redactURL(st.URL) + ", a page that names no job"
			w.e.Run.PostingFacts = append(w.e.Run.PostingFacts, "closed: "+note)
			return end(EndPostingClosed, note)
		}
		if hasForm && w.pages == 0 && w.opens == 0 {
			// A first page that names a different job than the evaluated report.
			if s := w.roleGuard(st); s != nil {
				return nil, s
			}
		}
		if !hasForm && w.pages == 0 {
			// Evidence point 7: a page that is a supported widget plus a plain
			// button is cleared through the user's own account, then the walk goes
			// on. A full-page Cloudflare interstitial is never touched. Once a form
			// page was walked, a widget beside a Submit is the submit stage's.
			if moved, s := w.r.challengeOnly(ctx, w, st); s != nil {
				return nil, s
			} else if moved {
				afterForm = false
				continue
			}
		}
		if n := MaintenanceNotice(st.Lines); n != "" && !hasForm {
			return nil, &stop{class: ClassUnavailable, reason: "the site is down for a while: " + n}
		}
		if n := BotInterstitial(st.Title, st.Lines); n != "" {
			return w.endBot(st, n)
		}
		if d := DetectChallenge(st); d != nil && !hasForm && d.ChallengeOnly && !w.finalStep(st) {
			if d.Kind == gates.ChallengeDataDome && !w.reloaded {
				// DataDome often lets the same address through on a second load:
				// one reload, never more.
				w.reloaded = true
				if _, err := w.s.tab.Navigate(ctx, st.URL); err == nil && w.sleep(ctx, w.tm.Poll) {
					afterForm = false
					continue
				}
			}
			return w.endBot(st, "a challenge stands where the page should be ("+d.Kind+")")
		}
		if !hasForm {
			switch {
			case ClosedNotice(st.Lines) != "":
				n := ClosedNotice(st.Lines)
				w.e.Run.PostingFacts = append(w.e.Run.PostingFacts, "closed: "+n)
				return end(EndPostingClosed, n)
			case AlreadyAppliedNotice(st.Lines) != "":
				return end(EndAlreadyApplied, AlreadyAppliedNotice(st.Lines))
			case EmailOnlyNotice(st.Lines) != "":
				return end(EndEmailApplication, EmailOnlyNotice(st.Lines))
			}
		}
		if !hasForm {
			if label := signInWall(st); label != "" {
				res, s := end(EndLoginRequired, fmt.Sprintf("the page asks to sign in to apply (%q)", label))
				if res != nil {
					res.NoAccount = isBoardURL(st.URL)
				}
				return res, s
			}
		}
		if w.pages == 0 && w.opens == 0 && w.e.Run.ATS == ATSGeneric && w.e.hops < maxHops && (hasForm || len(Categorize(st, false).Openers) > 0) {
			// A careers page's own forms and Apply links are its marketing when an
			// application frame is embedded (exiger: HubSpot lead forms beside
			// Greenhouse's iframe, which its script inserts late).
			if dest, why := w.awaitEmbedded(ctx, st); dest != "" {
				if s := w.r.handoff(ctx, w.e, w.s, dest, why, true); s != nil {
					return nil, s
				}
				w.opens, w.opened, afterForm = 0, map[string]bool{}, false
				continue
			}
		}
		if !hasForm {
			if dest, why := w.handoffTarget(st); dest != "" {
				if s := w.r.handoff(ctx, w.e, w.s, dest, why, true); s != nil {
					return nil, s
				}
				w.opens, w.opened, afterForm = 0, map[string]bool{}, false
				continue
			}
		}
		if hasForm && w.loginGate(st) {
			return end(EndLoginRequired, "the page asks for a sign-in the entry check did not see")
		}
		if hasForm && bareSignInGate(st) {
			// An email-only "log in or create account" page is never filled.
			return end(EndLoginRequired, "the page is a bare sign-in gate")
		}
		if l := dataConsentPage(st, w.pages > 0); l != "" {
			acted, why, s := w.dataConsent(ctx, st, l)
			if s != nil {
				return nil, s
			} else if acted {
				afterForm = false
				continue
			}
			return end(EndConsentGate, fmt.Sprintf("the page asks for consent to process your data before the form (%q); it is yours to give: %s", l, why))
		}
		if shown, s := w.showApplicationTab(ctx, st, hasForm); s != nil {
			return nil, s
		} else if shown {
			continue
		}
		if hasForm {
			if w.pages >= MaxPages {
				return end(EndMaxPages, fmt.Sprintf("%d pages walked", w.pages))
			}
			w.pages++
			if s := w.s.combinedType(ctx, st); s != nil {
				return nil, s
			}
			if s := w.s.pageWork(ctx, w, st); s != nil && !s.moved {
				return nil, s
			} else if s != nil {
				// The tab moved to another origin mid-fill (Procore's step into Workday): hand off or read it.
				if _, s := w.arrived(ctx); s != nil {
					return nil, s
				}
				continue
			}
			if st2, err := Probe(ctx, w.s.tab); err == nil {
				st = st2
				w.s.last = st
			}
		}
		cats := Categorize(st, w.pages > 0)
		if cats.OneClick != "" && !hasForm {
			return end(EndOneClickApply, "one-click apply is reported, never pressed")
		}
		if cats.ClosedLabel != "" {
			return end(EndPostingClosed, "the page's control reads: "+cats.ClosedLabel)
		}
		afterForm = hasForm

		nav := cats.Nav
		switch {
		case len(nav) > 0 && !(st.Step.Last() && len(cats.Submit) > 0):
			if w.pages >= MaxPages {
				return end(EndMaxPages, fmt.Sprintf("%d pages walked and the form continues", w.pages))
			}
			if w.e.Run.ATS == ATSLinkedIn {
				switch sessionChallenge(st) {
				case "login_redirect":
					return end(EndLoginRequired, "LinkedIn redirected to its login or checkpoint page ("+redactURL(st.URL)+")")
				case "captcha":
					return w.endBot(st, "a captcha challenge stands on the LinkedIn page")
				}
			}
			c := nav[w.decideNav(ctx, st, nav)]
			step := "pressed|" + stateKey(st) + "|" + controlKey(c)
			key := stateKey(st) + "|" + controlKey(c) + "|" + w.s.writesKey()
			// A step the page refused, read again with nothing new written, is
			// the same step: the page names what it lacks, and pressing again
			// sends what it already refused.
			again := w.s.writesKey() == "" && w.seen[step] > 0 && stallWords(st) != ""
			if w.seen[key]++; w.seen[key] >= 2 || again {
				if acted, s := w.lateCodeDialogs(ctx); s != nil {
					return nil, s
				} else if acted {
					delete(w.seen, key)
					continue
				}
				return end(EndStalled, "the step returned with the same control and writes"+stallWords(st))
			}
			w.seen[step]++
			moved, s := w.press(ctx, st, c)
			if s != nil {
				return nil, s
			}
			w.s.combinedSettled(ctx)
			if !moved {
				// Evidence point 3: the Next raised a puzzle and the page stayed put.
				if after, perr := Probe(ctx, w.s.tab); perr == nil {
					if m2, s2 := w.r.nextRaised(ctx, w, after, c); s2 != nil {
						return nil, s2
					} else if m2 {
						continue
					}
				}
				if w.seen[key+"|nomove"]++; w.seen[key+"|nomove"] >= 2 {
					if acted, s := w.lateCodeDialogs(ctx); s != nil {
						return nil, s
					} else if acted {
						delete(w.seen, key+"|nomove")
						continue
					}
					return end(EndStalled, "the control did not move the page"+stallWords(st))
				}
			}
		case len(cats.Submit) == 0 && w.s.combinedCreate(st) != nil:
			if _, s := w.pressAs(ctx, st, *w.s.combinedCreate(st), gates.StageAccount); s != nil {
				return nil, s
			}
			w.s.combinedSettled(ctx)
		case len(cats.Submit) > 0:
			if cats.Submit[0].Disabled && len(st.Validation) == 0 {
				again, s := w.enabledLater(ctx)
				if s != nil {
					return nil, s
				}
				if again != nil {
					st, cats = again, Categorize(again, w.pages > 0)
					w.s.last = st
				}
			}
			if cats.Submit[0].Disabled {
				if again, still := w.revalidate(ctx, st); again != nil {
					st, cats = again, Categorize(again, w.pages > 0)
					w.s.last = st
					if len(cats.Submit) > 0 && !cats.Submit[0].Disabled {
						return end(EndSubmit, readyForSubmit(cats))
					}
					return end(EndSubmitDisabled, "the Submit control is disabled"+validationWords(st)+still)
				}
				return end(EndSubmitDisabled, "the Submit control is disabled"+validationWords(st))
			}
			// Workday's Review recap prints "No Response" for a required answer it
			// does not hold (a Work Experience row lost after its own page).
			if p, err := ReadPrePress(ctx, w.s.tab); err == nil && len(p.NoResponse) > 0 {
				return end(EndStalled, `the Review page shows "No Response" for: `+strings.Join(p.NoResponse, ", "))
			}
			return end(EndSubmit, readyForSubmit(cats))
		case postingApplyLink(st) != "" && !w.decided["link|"+postingApplyLink(st)]:
			// The posting's own named Apply link, before any bare Apply that may
			// be the site menu's.
			if _, s := w.openLink(ctx, postingApplyLink(st)); s != nil {
				return nil, s
			}
			afterForm = false
		case len(cats.Openers) > 0 && w.opens < maxOpens:
			c := w.pickOpener(ctx, st, cats.Openers)
			if c == nil {
				return end(EndNoNavigation, "every Apply control was already followed")
			}
			w.opens++
			w.opened[controlKey(*c)] = true
			if popup, moved, s := w.pressOpener(ctx, st, *c); s != nil {
				return nil, s
			} else if popup != "" {
				if s := w.r.handoff(ctx, w.e, w.s, popup, "the Apply control opened it in a new window", true); s != nil {
					return nil, s
				}
				w.opens, w.opened, afterForm = 0, map[string]bool{}, false
			} else if w.coveredBy != "" {
				return end(EndNoNavigation, fmt.Sprintf("the Apply control %q is covered by %q and no consent gate that closes with the least consent was found", c.Labels[0], w.coveredBy))
			} else if moved {
				w.awaitOpenerGone(ctx, st, *c)
				if handed, s := w.arrived(ctx); s != nil {
					return nil, s
				} else if handed {
					w.opens, w.opened, afterForm = 0, map[string]bool{}, false
				}
			}
		case hasForm:
			if acted, s := w.moreOnThisPage(ctx, st); s != nil {
				return nil, s
			} else if acted {
				afterForm = false
				continue
			}
			// A consent dialog whose buttons outrank the page hides the form's own
			// controls (Toast's cookie dialog over its Apply): it is closed with
			// the least consent and the page read again.
			if cleared, s := w.closeConsent(ctx, st.URL); s != nil {
				return nil, s
			} else if cleared {
				continue
			}
			return end(EndNoNavigation, "the form has no navigation or Submit control the walk may press")
		default:
			if cleared, s := w.closeConsent(ctx, st.URL); s != nil {
				return nil, s
			} else if cleared {
				afterForm = false
				continue
			}
			if blankPage(st) {
				// A document with no text and no control never loaded: that is
				// the site or the browser, never a fact about the posting.
				return nil, &stop{class: ClassUnavailable, reason: "the page loaded blank (" + redactURL(st.URL) + "): no text and no control were drawn"}
			}
			if stuck := st.FlowLoading && !siteErrorPage(st); stuck || siteErrorPage(st) {
				// The site's own error page, or a Workday step still loading when the
				// look gave up, is reloaded once per address and the steps it reopens
				// (Workday: its first) are walked again; a second one is the site's.
				if key := "reload|" + st.URL; !w.decided[key] {
					w.decided[key] = true
					if _, err := w.s.tab.NavigateDOM(ctx, st.URL); err != nil {
						return nil, w.browserStop(err)
					}
					afterForm, w.seen = false, map[string]int{}
					continue
				}
				what := "showed its error page again"
				if stuck {
					what = "still had not drawn the step"
				}
				return nil, &stop{class: ClassUnavailable, reason: "the site " + what + " after a reload (" + redactURL(st.URL) + ")"}
			}
			if len(cats.Openers) == 0 && findWall(st) != nil {
				// A sign-in wall after an Apply press (Workday's Google and "Sign
				// in with email", no form) is the account stage's to clear.
				return end(EndLoginRequired, "the page is a sign-in wall")
			}
			if opened, s := w.openLink(ctx, resumeLink(st)); s != nil {
				return nil, s
			} else if opened {
				afterForm = false
				continue
			}
			kind, note := PageUnknown, ""
			if len(cats.Openers) == 0 {
				kind, note = w.decideEnd(ctx, st)
			}
			switch kind {
			case PageClosed:
				w.e.Run.PostingFacts = append(w.e.Run.PostingFacts, "closed: "+note)
				return end(EndPostingClosed, note)
			case PageSignIn:
				return end(EndLoginRequired, note)
			default:
				if label := unrecognisedApply(st); label != "" {
					// An Apply-shaped control the walk does not follow is a gap in
					// the walk, not a fact about the posting.
					return &walkResult{End: EndNoForm, Detail: fmt.Sprintf("the page holds no application form; the Apply-shaped control %q was not recognised as an opener", label), Offers: true, Last: st}, nil
				}
				return end(EndNoForm, strings.TrimSpace("the page holds no application form; "+w.sentAway(st)+note))
			}
		}
	}
	st := w.s.last
	return &walkResult{End: EndStalled, Detail: "the walk made no progress", Last: st}, nil
}

// endBot ends the walk as a bot check; the tab is kept for the user.
func (w *walker) endBot(st *PageState, why string) (*walkResult, *stop) {
	if w.e.demoRefusal != "" {
		why += "; " + w.e.demoRefusal
	}
	cats := Categorize(st, w.pages > 0)
	return &walkResult{End: EndBotChallenge, Detail: "bot check: " + why, Offers: len(cats.Openers) > 0, Last: st}, nil
}

// readyForSubmit is the walk-end detail of a form that stands at its Submit; a
// final control that reads Apply says so.
func readyForSubmit(cats Categories) string {
	if len(cats.Submit) > 0 && cats.Submit[0].FinalApply {
		return fmt.Sprintf("the form is ready for Submit (its final control reads %q)", cats.Submit[0].Labels[0])
	}
	return "the form is ready for Submit"
}

// awaitOpenerGone gives a pressed Apply control time to take the page away.
// The page's signature can change for reasons that are not the press (a
// share widget or a frame finishing its own load), and then the page still
// shows the control for a moment: reading it again at once would find the
// control, which was followed already, and end the walk on a page that is
// about to move. The wait ends as soon as the control, or the page, is gone.
func (w *walker) awaitOpenerGone(ctx context.Context, st *PageState, c Control) {
	key, url := controlKey(c), redactURL(st.URL)
	deadline := time.Now().Add(w.tm.PressWait)
	for time.Now().Before(deadline) {
		if !w.sleep(ctx, w.tm.Poll) {
			return
		}
		cur, err := Probe(ctx, w.s.tab)
		if err != nil || redactURL(cur.URL) != url || cur.HasForm() {
			return // the document is going away or already gone, or the form is drawn
		}
		still := false
		for _, o := range Categorize(cur, w.pages > 0).Openers {
			if controlKey(o) == key {
				still = true
			}
		}
		if !still {
			return
		}
	}
}

// validationWordsShown bounds the page's own messages quoted in a stop reason.
const validationWordsShown = 4

// validationWords is what the page says about a refused step. An invalid mark
// counts only when it sits on a control of the step that was sent (a mark on a
// control of another step or of the site's chrome says nothing about this
// press) and is named as `N field(s) marked invalid: "<label>"`, unless a
// message already names that field; a mark with no name says nothing. Any
// other message is quoted as the page worded it (an error summary's entries
// arrive field-named from the probe), once each and at most
// validationWordsShown of them.
func validationWords(st *PageState) string {
	if len(st.Validation) == 0 {
		return ""
	}
	labels := map[string]string{}
	for _, in := range st.Inputs {
		if !in.Furniture && strings.TrimSpace(in.Label) != "" {
			labels[normText(in.Label)] = strings.TrimSpace(in.Label)
		}
	}
	var names, words []string
	seen := map[string]bool{}
	for _, v := range st.Validation {
		v = strings.TrimSpace(v)
		if v == "invalid" {
			continue
		}
		if name, ok := strings.CutSuffix(v, " invalid"); ok {
			if l, on := labels[normText(name)]; on {
				names = append(names, l)
			}
			continue
		}
		if k := normText(v); !seen[k] {
			seen[k] = true
			words = append(words, v)
		}
	}
	said := normText(strings.Join(words, " "))
	var marked []string
	for _, l := range names {
		if !strings.Contains(said, normText(l)) {
			marked = append(marked, strconv.Quote(l))
		}
	}
	var parts []string
	if len(marked) > 0 {
		parts = append(parts, fmt.Sprintf("%d field(s) marked invalid: %s", len(marked), strings.Join(marked, ", ")))
	}
	if len(words) > validationWordsShown {
		words = append(words[:validationWordsShown:validationWordsShown], fmt.Sprintf("and %d more", len(words)-validationWordsShown))
	}
	if len(words) > 0 {
		parts = append(parts, "the page says: "+strings.Join(words, " / "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

// loginGate reports a sign-in form: a password field with no application
// fields beside it other than the credential boxes.
func (w *walker) loginGate(st *PageState) bool {
	pw, other := 0, 0
	for _, in := range st.Inputs {
		if in.Furniture {
			continue
		}
		switch {
		case in.Type == "password":
			pw++
		case in.Type == "email" || in.Type == "checkbox" || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete), "user") || strings.Contains(strings.ToLower(in.Label+in.Name), "email"):
		default:
			other++
		}
	}
	return pw > 0 && other <= 1
}

// roleGuard stops a run whose first page names a different job than the
// evaluated report, as a stale link.
func (w *walker) roleGuard(st *PageState) *stop {
	run := w.e.Run
	want, company := run.Input.Role, run.Input.Company
	if run.Report != nil {
		want, company = run.Report.Role, run.Report.Company
	}
	role := PageRole(st.Title, st.Headings, "")
	if StaleLink(role, want, company) {
		return &stop{class: ClassPosting, reason: fmt.Sprintf("stale link: the page names %q, not the evaluated role", role)}
	}
	return nil
}

// maxProbeHref is the length the probe clips a link's address to.
const maxProbeHref = 160

// coveredLinkTarget is the address a covered link leads to, on the page's own
// host; empty for any other control, another host, or an address the probe
// clipped.
func coveredLinkTarget(c Control, pageURL string) string {
	if c.Tag != "a" || c.Href == "" || len(c.Href) >= maxProbeHref {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(c.Href)
	if err != nil {
		return ""
	}
	dest := base.ResolveReference(ref)
	if (dest.Scheme != "https" && dest.Scheme != "http") || !strings.EqualFold(dest.Hostname(), base.Hostname()) {
		return ""
	}
	return dest.String()
}

// press presses one navigation control through gates.PressControl: its label
// is read at press time, the decision is audited, and a click that the browser
// reports as obstructed counts as a press that did not happen. It reports
// whether the page moved.
func (w *walker) press(ctx context.Context, st *PageState, c Control) (bool, *stop) {
	return w.pressAs(ctx, st, c, c.walkStage())
}

func (w *walker) pressAs(ctx context.Context, st *PageState, c Control, stage gates.ControlStage) (bool, *stop) {
	loc, err := Locate(ctx, w.s.tab, c.I)
	if err != nil {
		return false, w.browserStop(err)
	}
	if !loc.Found || !loc.Visible {
		if c, loc, err = w.refind(ctx, st, c); err != nil {
			return false, w.browserStop(err)
		}
	}
	w.coveredBy = ""
	for stage == gates.StageWalk && loc.Found && loc.Visible && !loc.Hit {
		// Something stands over the control. A consent gate (a cookie notice with
		// a backdrop) is closed with the least consent and the control is found
		// again, gate after gate up to maxConsents; any other cover is reported.
		cleared, s := w.closeConsent(ctx, st.URL)
		if s != nil {
			return false, s
		}
		if !cleared {
			break
		}
		if loc, err = Locate(ctx, w.s.tab, c.I); err != nil {
			return false, w.browserStop(err)
		}
		if !loc.Found || !loc.Visible {
			// The page redrew the control once the gate closed (Avature).
			if c, loc, err = w.refind(ctx, st, c); err != nil {
				return false, w.browserStop(err)
			}
		}
	}
	host, _ := parseHost(st.URL)
	d := gates.PressControl(gates.ControlInput{Stage: stage, Found: loc.Found && loc.Visible, Labels: loc.Labels, TopHost: host, InFrame: c.I >= frameBase, FinalApply: c.FinalApply, InDialog: c.Modal, Tab: c.Role == "tab", Unattended: w.e.unattended()})
	if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
		return false, &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !d.Allowed() {
		return false, &stop{class: ClassDefect, reason: "control not pressed: " + d.Reason}
	}
	if !loc.Hit && stage == gates.StageWalk && loc.Found && loc.Visible {
		// A link something other than a consent gate stands over (a chat
		// assistant's panel) is followed: its own address is where the press leads.
		if dest := coveredLinkTarget(c, st.URL); dest != "" {
			before, _ := Signature(ctx, w.s.tab)
			if _, err := w.s.tab.NavigateDOM(ctx, dest); err != nil {
				return false, w.browserStop(err)
			}
			return w.moved(ctx, before, nil), nil
		}
	}
	if !loc.Hit {
		if loc.Found && loc.Visible {
			w.coveredBy = firstNonEmpty(loc.Blocker, "another element")
		}
		return false, nil
	}
	before, serr := Signature(ctx, w.s.tab)
	if err := w.clickPage(ctx, loc); err != nil && !lateClick(err) {
		return false, w.browserStop(err)
	}
	if serr != nil {
		before = "" // moved() takes the first fingerprint it can read as the baseline
	}
	return w.moved(ctx, before, st.ActiveStep), nil
}

// maxRefinds is how many times a press looks the control up again when the
// page was redrawn after it was read.
const maxRefinds = 3

// refind finds again a control whose handle the page dropped between the read
// and the press: a page that redraws itself right after it loads (Taleo's job
// page replaces its buttons) leaves the handle pointing at a node that is gone.
// The page is read again and the control is the one with the same labels, tag,
// role and address (the same occurrence when the page shows several alike).
// It never relaxes the press: the gate reads the control's label at the new
// press time like any other. A control that is not found again comes back as
// it was, and the gate refuses it as not found.
func (w *walker) refind(ctx context.Context, st *PageState, c Control) (Control, Located, error) {
	var miss Located
	nth, key := 0, refindKey(c)
	for _, o := range st.Controls {
		if o.I == c.I {
			break
		}
		if refindKey(o) == key {
			nth++
		}
	}
	for try := 0; try < maxRefinds; try++ {
		if !w.sleep(ctx, w.tm.Poll) {
			return c, miss, nil
		}
		cur, err := Probe(ctx, w.s.tab)
		if err != nil {
			continue // the document is mid-navigation: read it again
		}
		var same []Control
		for _, o := range cur.Controls {
			if refindKey(o) == key {
				same = append(same, o)
			}
		}
		if len(same) == 0 {
			continue
		}
		o := same[min(nth, len(same)-1)]
		o.FinalApply = c.FinalApply
		loc, err := Locate(ctx, w.s.tab, o.I)
		if err != nil {
			return c, miss, err
		}
		if loc.Found && loc.Visible {
			w.s.last = cur
			return o, loc, nil
		}
	}
	return c, miss, nil
}

// closeConsent closes the consent gate standing on the page, if any, with the
// least consent that closes it (the cookie-consent gate decides which button,
// writes its decision to the audit, and an accept-all is the last resort).
// It reports whether a button was pressed.
func (w *walker) closeConsent(ctx context.Context, pageURL string) (bool, *stop) {
	if w.consents >= maxConsents {
		return false, nil
	}
	g, err := ReadConsent(ctx, w.s.tab)
	if err != nil || !g.Found {
		return false, nil
	}
	// Only a control nothing covers is a choice: of two stacked gates (a notice
	// under a cookie dialog) the top one is closed first.
	open := g.Controls[:0:0]
	for _, b := range g.Controls {
		if l, lerr := Locate(ctx, w.s.tab, b.I); lerr == nil && l.Found && l.Visible && l.Hit {
			open = append(open, b)
		}
	}
	g.Controls = open
	labels := make([]string, len(g.Controls))
	for i, b := range g.Controls {
		labels[i] = b.Label
	}
	d := gates.CloseConsentGate(gates.CookieInput{Controls: labels, PreferenceCenter: g.Preference, AllOptionalOff: g.AllOff, AsksAboutApplicant: g.Asks})
	if aerr := w.r.audit(w.e, d.Decision, pageURL, false); aerr != nil {
		return false, &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !d.Allowed() || d.Press < 0 || d.Press >= len(g.Controls) {
		return false, nil
	}
	loc, err := Locate(ctx, w.s.tab, g.Controls[d.Press].I)
	if err != nil {
		return false, w.browserStop(err)
	}
	if !loc.Found || !loc.Visible || !loc.Hit {
		return false, nil
	}
	if err := w.s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return false, w.browserStop(err)
	}
	w.consents++
	if !w.sleep(ctx, time.Second) {
		return false, cancelledStop(StageFill)
	}
	return true, nil
}

// moved waits for a pressed control to change the page. On a page whose
// progress bar names its step, the page has moved once the bar names another
// one: Workday redraws the step it is leaving (a saving state, its text) before
// the next step comes, and a walk that read that redraw would fill the old step
// again and press Next on the new one before it drew. A step that only redraws
// itself for the whole wait (a refusal) still counts as changed.
func (w *walker) moved(ctx context.Context, before string, step *string) bool {
	deadline := time.Now().Add(w.tm.PressWait)
	changed := false
	for time.Now().Before(deadline) {
		if !w.sleep(ctx, 250*time.Millisecond) {
			return false
		}
		sig, err := Signature(ctx, w.s.tab)
		if err != nil {
			continue
		}
		if before == "" {
			before = sig
			continue
		}
		if sig == before {
			continue
		}
		if step == nil {
			return true
		}
		changed = true
		var now string
		if err := w.s.tab.Eval(ctx, probeExpr("activeStep", nil), &now); err == nil && now != *step {
			return true
		}
	}
	return changed
}

// stateKey identifies a step across visits: its address, its step marker and
// the questions it asks, so two different steps of one wizard are not taken for
// one step that returned.
func stateKey(st *PageState) string {
	var b strings.Builder
	b.WriteString(redactURL(st.URL))
	if st.ActiveStep != nil {
		b.WriteString("|" + *st.ActiveStep)
	}
	if st.Step != nil {
		fmt.Fprintf(&b, "|%d/%d", st.Step.Current, st.Step.Total)
	}
	for _, in := range st.Inputs {
		b.WriteString("|" + in.Label + in.Name)
	}
	if len(st.Inputs) == 0 {
		// A summary-only step (LinkedIn's review) asks nothing: its modal text
		// is what tells it from the step before.
		for _, d := range st.Dialogs {
			b.WriteString("|" + d.Heading + d.Text)
		}
	}
	return b.String()
}

// completeCodePane completes a code pane that is already showing when a page
// arrives: the code is typed and the Verify control waited for up to 3
// seconds and never clicked while disabled, before the page is read.
func (w *walker) completeCodePane(ctx context.Context, st *PageState) (bool, *stop) {
	r := w.r
	if r.d.Page.Codes == nil || (w.e.Run.ATS == ATSGreenhouse && !w.e.Run.Pressed) {
		return false, nil
	}
	boxes, _ := codeBoxes(st)
	if len(boxes) == 0 {
		return false, nil
	}
	fields, _ := st.FormFields()
	if fields > len(boxes) {
		return false, nil // a code box among other questions is the check lane's
	}
	code, _, err := w.e.codeLane().Lookup(ctx, r.d.Page.Codes, r.d.Page.Code, r.d.Now, time.Time{}, w.s.codeDomain(), fitsBoxes(len(boxes)))
	if err != nil {
		return false, nil
	}
	if err := typeCode(ctx, w.s.tab, boxes, code); err != nil {
		return false, nil
	}
	deadline := time.Now().Add(r.d.Page.Code.withDefaults().Verify)
	for time.Now().Before(deadline) {
		cur, perr := Probe(ctx, w.s.tab)
		if perr != nil {
			break
		}
		for _, c := range cur.Controls {
			if c.Disabled {
				continue
			}
			for _, l := range c.Labels {
				if strings.EqualFold(l, "verify") || strings.EqualFold(l, "continue") || strings.EqualFold(l, "next") {
					_, s := w.pressAs(ctx, cur, c, gates.StageAccount)
					return true, s
				}
			}
		}
		if !w.sleep(ctx, 100*time.Millisecond) {
			break
		}
	}
	return true, nil
}

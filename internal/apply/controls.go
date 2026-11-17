package apply

import (
	"context"
	"net/url"
	"slices"
	"sort"
	"strings"

	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

var (
	furnitureLabelRE = lazyregexp.New(`(?i)\b(?:join\s+(?:our\s+|the\s+)?(?:talent|community|network)|talent\s+(?:community|network|pool)|job\s+alerts?|(?:get|sign\s+up\s+for|create)\s+(?:job\s+)?alerts?|newsletter|subscribe|search\s+jobs?)\b`)
	oneClickRE       = lazyregexp.New(`(?i)\b(?:1|one)[-\s]?click\s+apply\b|\bapply\s+with\s+one\s+click\b`)
	closedLabelRE    = lazyregexp.New(`(?i)\b(?:closed|no\s+longer\s+(?:available|accepting)|expired|position\s+filled|not\s+accepting)\b`)
)

// Categories sorts a page's controls by what the walk may do with them.
type Categories struct {
	// Submit are final-Submit controls of the application form.
	Submit []Control
	// Nav are navigation controls inside the form (Next, Continue, Review).
	Nav []Control
	// Openers are Apply controls that sit in no form holding a visible field
	// or file input: they reveal the form and are never the final Submit.
	Openers []Control
	// OneClick is set when the page offers a one-click apply, which is
	// reported and never pressed.
	OneClick string
	// ClosedLabel is a disabled control whose label states the posting is
	// closed.
	ClosedLabel string
	// InFormApply are Apply-labelled controls inside a form that has fields:
	// they could send the application, so the walk never presses them.
	InFormApply []Control
}

// submitIDRE is a control id that names the form's submit, the proof a button
// that is not of type submit sends the form (SuccessFactors draws its final
// Apply as a span with role=button and id "491:_submitBtn").
var submitIDRE = lazyregexp.New(`(?i)(?:^|[^a-z])submit(?:btn|button)?$`)

// finalApply picks the form's own final control from the in-form Apply
// controls: a button of type submit, or one whose id names the submit, that
// reads as an Apply verb, on a filled page that offers no Next or Review beside
// it and no opener. Such a button ends the form the way Submit does, so it is
// listed as the form's Submit with
// FinalApply set; the submit stage presses it through the same gates and the
// walk never does.
func finalApply(cats *Categories, afterForm bool) {
	if !afterForm || len(cats.Nav) > 0 || len(cats.Openers) > 0 || len(cats.Submit) > 0 {
		return
	}
	for _, c := range cats.InFormApply {
		if ((c.Tag == "button" || c.Tag == "input") && c.Type == "submit" || submitIDRE.MatchString(c.ID)) && gates.IsFinalApply(c.Labels) {
			c.FinalApply = true
			cats.Submit = append(cats.Submit, c)
			return
		}
	}
}

// formHasFiles reports a form that takes a file (a résumé upload); a page
// whose fields sit in no form reads its own inputs.
func (st *PageState) formHasFiles(i int) bool {
	if i >= 0 && i < len(st.Forms) {
		return st.Forms[i].Files > 0 || st.Forms[i].HasResume
	}
	for _, in := range st.Inputs {
		if in.Type == "file" {
			return true
		}
	}
	return false
}

func (st *PageState) formHasFields(i int) bool {
	if i < 0 || i >= len(st.Forms) {
		return false
	}
	f := st.Forms[i]
	return !f.Furniture && (f.Fields > 0 || f.Files > 0)
}

// modalInputs reports whether a modal holds fillable inputs.
func (st *PageState) modalInputs() bool {
	for _, in := range st.Inputs {
		if in.Modal && !in.Furniture {
			return true
		}
	}
	return false
}

// Categorize applies the walk's rules to a page. afterForm is true once the
// walk has filled a page of this application. A control in an open modal
// outranks every control behind it. A Submit-looking control of a code-entry
// dialog confirms the code and is never the form's Submit. Site furniture (search and alert forms,
// footer and navigation controls, chat widgets) is never a target, and a
// control that reads as Withdraw or Discard is never offered.
func Categorize(st *PageState, afterForm bool) Categories {
	var cats Categories
	var cands []Control
	for _, c := range st.Controls {
		if c.Chat || (c.Chrome && !c.Modal) {
			continue
		}
		if c.Form >= 0 && c.Form < len(st.Forms) && st.Forms[c.Form].Furniture {
			continue
		}
		if furnitureLabelRE.MatchString(strings.Join(c.Labels, " ")) {
			continue
		}
		cands = append(cands, c)
	}
	anyModal := false
	for _, c := range cands {
		if c.Modal {
			anyModal = true
		}
	}
	if anyModal {
		var m []Control
		for _, c := range cands {
			if c.Modal {
				m = append(m, c)
			}
		}
		cands = m
	}
	// The exclusion comes after the modal rule: the pane stays the open modal
	// whose controls outrank the page behind it.
	codeSubmits := codeDialogSubmits(st)
	var oneClicks []Control
	for _, c := range cands {
		label := strings.Join(c.Labels, " ")
		if codeSubmits[c.I] && gates.ClassifyControl(c.Labels...) == gates.ControlSubmit {
			continue
		}
		if oneClickRE.MatchString(label) {
			oneClicks = append(oneClicks, c)
			continue
		}
		if applicationTab(c) {
			cats.Openers = append(cats.Openers, c)
			continue
		}
		if c.Disabled && closedLabelRE.MatchString(label) && gates.ClassifyControl(c.Labels...) != gates.ControlSubmit {
			cats.ClosedLabel = c.Labels[0]
			continue
		}
		// A control in a form that holds fields, in a modal that holds inputs,
		// or on a page whose fields sit in no <form> element at all (a
		// single-page application) is part of the application.
		inForm := st.formHasFields(c.Form) || (c.Modal && st.modalInputs()) || (c.Form < 0 && st.HasForm())
		switch gates.ClassifyControl(c.Labels...) {
		case gates.ControlSubmit:
			if c.Modal && st.profileDialog() {
				c.ProfileStep = true
				cats.Nav = append(cats.Nav, c)
				continue
			}
			// A read-only review step has no field, but it follows a page that
			// held the form; its Submit is the form's.
			if inForm || st.HasForm() || afterForm {
				cats.Submit = append(cats.Submit, c)
			}
		case gates.ControlNavigation:
			switch {
			case isApplyLabel(c.Labels) && inForm && stepNameRE.MatchString(c.Name) && !st.formHasFiles(c.Form):
				// A form's Apply named for its next step moves the application on
				// (Procore's name-and-email step). A form that takes a file is the
				// application itself (Toast's, on the same markup): its Apply sends it.
				cats.Nav = append(cats.Nav, c)
			case isApplyLabel(c.Labels) && inForm:
				cats.InFormApply = append(cats.InFormApply, c)
			case inForm || (afterForm && !opensApplication(c.Labels)):
				// A read-only review or summary step of a multi-step application
				// holds no field, but its Next belongs to the application the walk
				// is in: it is not an Apply control that opens one.
				cats.Nav = append(cats.Nav, c)
			default:
				cats.Openers = append(cats.Openers, c)
			}
		case gates.ControlOther:
			// A filled form's own "Send" (Recruitee), or the final Apply of a page whose
			// fields sit in no <form> (Gem); a dialog's Send is the code gates'.
			if !c.Modal && inForm && gates.IsFinalApply(c.Labels) {
				cats.InFormApply = append(cats.InFormApply, c)
			}
		}
	}
	// A one-click Apply counts only as this posting's: never in an <aside>
	// rail, nor one of several job cards.
	cards := map[int]bool{}
	for _, c := range oneClicks {
		if c.Card > 0 {
			cards[c.Card] = true
		}
	}
	for _, c := range oneClicks {
		if !c.Aside && (c.Card == 0 || len(cards) < 2) {
			cats.OneClick = c.Labels[0]
			break
		}
	}
	cats.Openers = slices.DeleteFunc(cats.Openers, func(c Control) bool { return homeLink(st.URL, c.Href) })
	cats.Openers = thisPosting(cats.Openers)
	sortOpeners(st, cats.Openers)
	finalApply(&cats, afterForm)
	return cats
}

// stepNameRE reads a control name that says it moves to the next step.
var stepNameRE = lazyregexp.New(`(?i)^next[_-]?step$`)

// opensApplication reports a navigation label that starts or resumes an
// application (Apply, Start, Complete Your Application) rather than moving
// through one.
func opensApplication(labels []string) bool {
	if isApplyLabel(labels) {
		return true
	}
	for _, l := range labels {
		if strings.Contains(strings.ToLower(l), "complete your") {
			return true
		}
	}
	return false
}

// isApplyLabel reads the wording of an Apply control: apply, start, or the
// "I'm interested" a SmartRecruiters posting puts on its Apply link.
func isApplyLabel(labels []string) bool {
	for _, l := range labels {
		l = strings.ToLower(l)
		if strings.Contains(l, "apply") || strings.Contains(l, "start") || strings.Contains(l, "interested") {
			return true
		}
	}
	return false
}

var (
	applyShapedRE = lazyregexp.New(`(?i)\b(?:apply|interested)\b`)
	filterLabelRE = lazyregexp.New(`(?i)\bfilters?\b`)
)

// unrecognisedApply returns the label of a visible control that reads as an
// Apply control but that the walk neither follows, reports nor treats as the
// form's own: a page that holds one is not a posting with nothing to apply
// to, it is a gap in the walk.
func unrecognisedApply(st *PageState) string {
	for _, c := range st.Controls {
		if c.Chat || c.Disabled || (c.Chrome && !c.Modal) {
			continue
		}
		if c.Form >= 0 && c.Form < len(st.Forms) && st.Forms[c.Form].Furniture {
			continue
		}
		label := strings.Join(c.Labels, " ")
		if !applyShapedRE.MatchString(label) || filterLabelRE.MatchString(label) || furnitureLabelRE.MatchString(label) || oneClickRE.MatchString(label) {
			continue
		}
		if gates.ClassifyControl(c.Labels...) == gates.ControlOther {
			return c.Labels[0]
		}
	}
	return ""
}

// resumeLinkRE is the whole folded text of a link that resumes an application
// the site holds unfinished.
var resumeLinkRE = lazyregexp.New(`^\W*(?:complete|continue|finish|resume)(?: (?:my|your))? application\W*$`)

// resumeLink returns the address of a link on the page's own site whose words
// resume an application the site holds, or "".
func resumeLink(st *PageState) string {
	for _, l := range st.Links {
		if resumeLinkRE.MatchString(normText(l.Text)) && sameSite(hostOf(l.Href), hostOf(st.URL)) {
			return l.Href
		}
	}
	return ""
}

// postingNumberRE is a posting's number in its page's path (Elastic's
// ".../agentic-ai-engineer/8079636").
var postingNumberRE = lazyregexp.New(`\d{5,}`)

// postingApplyLink returns the address of a link on a formless page's own site
// that applies for what it names (Elastic's "Apply Now for Agentic AI
// Engineer", beside its menu's bare "Apply Now" to the careers home) and whose
// address carries the posting's number from the page's path, or "".
func postingApplyLink(st *PageState) string {
	nums := postingNumberRE.FindAllString(urlPath(st.URL), -1)
	if len(nums) == 0 || st.HasForm() {
		return ""
	}
	for _, l := range st.Links {
		if gates.NamedApply(l.Text) && sameSite(hostOf(l.Href), hostOf(st.URL)) &&
			slices.ContainsFunc(nums, func(n string) bool { return strings.Contains(l.Href, n) }) {
			return l.Href
		}
	}
	return ""
}

// openLink opens, once, the address of a link whose words the walk may not
// press: one that resumes the application the site holds unfinished
// (Avature's "Complete Application", resumeLink) or the posting's named Apply
// (postingApplyLink). A GET never submits, while a press of those words could
// be a final Submit elsewhere.
func (w *walker) openLink(ctx context.Context, href string) (bool, *stop) {
	if href == "" || w.decided["link|"+href] {
		return false, nil
	}
	w.decided["link|"+href] = true
	if _, err := w.s.tab.NavigateDOM(ctx, href); err != nil {
		return false, w.browserStop(err)
	}
	return true, nil
}

// blankPage reports a document that drew nothing: no text, no control, no
// field, no frame and no link.
func blankPage(st *PageState) bool {
	return strings.TrimSpace(st.Text) == "" && len(st.Controls) == 0 && len(st.Inputs) == 0 && len(st.Frames) == 0 && len(st.Links) == 0
}

// applicationTab reports a tab that shows the application and is not shown
// yet (Airbnb's "Application" beside "Role overview", whose panel holds the
// Greenhouse frame): pressing a tab only switches the panel, so it is an
// opener, and a selected one is never pressed again.
func applicationTab(c Control) bool {
	return c.Role == "tab" && c.Selected != "true" && !c.Disabled && gates.IsApplicationTab(c.Labels)
}

// showApplicationTab presses the page's application tab while another tab's
// panel is shown and the form reads in the hidden one (Fivetran's Greenhouse
// frame), so the form is filled once, as the applicant sees it.
func (w *walker) showApplicationTab(ctx context.Context, st *PageState, hasForm bool) (bool, *stop) {
	if !hasForm || w.opens >= maxOpens {
		return false, nil
	}
	for _, c := range Categorize(st, false).Openers {
		if applicationTab(c) {
			w.opens++
			w.opened[controlKey(c)] = true
			_, _, s := w.pressOpener(ctx, st, c)
			return s == nil, s
		}
	}
	return false, nil
}

// homeLink reports a link that opens the home page of the page's own site. A
// menu's "Apply Now" that leads to the careers home (Elastic's, drawn before
// the posting's own Apply links) opens no posting's application.
func homeLink(page, href string) bool {
	base, err := url.Parse(page)
	if err != nil || strings.TrimSpace(href) == "" {
		return false
	}
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return false
	}
	u := base.ResolveReference(ref)
	return (u.Scheme == "http" || u.Scheme == "https") && sameSite(u.Hostname(), base.Hostname()) &&
		strings.Trim(u.Path, "/") == "" && u.RawQuery == "" && strings.Trim(u.Fragment, "/!") == ""
}

// thisPosting drops the Apply controls of other jobs: a label offered in two
// or more different job cards is a list of jobs (ZipRecruiter's rail,
// "similar jobs"), while the same label twice in one card is the posting's
// own Apply at its top and bottom (Dell), and so is a label whose every copy
// opens one address (IBM's summary, drawn in its banner and its side rail).
func thisPosting(o []Control) []Control {
	cards, hrefs := map[string]map[int]bool{}, map[string]map[string]bool{}
	key := func(c Control) string { return strings.ToLower(strings.Join(strings.Fields(c.Labels[0]), " ")) }
	for _, c := range o {
		if c.Card > 0 {
			k := key(c)
			if cards[k] == nil {
				cards[k], hrefs[k] = map[int]bool{}, map[string]bool{}
			}
			cards[k][c.Card], hrefs[k][c.Href] = true, true
		}
	}
	return slices.DeleteFunc(o, func(c Control) bool {
		k := key(c)
		return c.Card > 0 && len(cards[k]) >= 2 && (len(hrefs[k]) > 1 || hrefs[k][""])
	})
}

// pickOpener chooses the Apply control to follow: the first one not followed
// yet, unless two or more remain and the page decision picks another.
func (w *walker) pickOpener(ctx context.Context, st *PageState, os []Control) *Control {
	var left []Control
	for _, c := range os {
		if !w.opened[controlKey(c)] {
			left = append(left, c)
		}
	}
	if len(left) == 0 {
		// ADP signs the applicant in through its code dialogs and lands back on
		// the posting, which offers only the opener already followed: it is
		// followed once more, never a third time.
		if (w.codeEntries > 0 || w.codePanes > 0 || w.e.deliverySent) && !w.reopened {
			w.reopened = true
			return &os[w.decideNav(ctx, st, os)]
		}
		return nil
	}
	return &left[w.decideNav(ctx, st, left)]
}

// sortOpeners ranks Apply controls: one outside an <aside> rail over one in
// it, a button over a link, a link that names the current posting or an
// apply path over any other, so a card for another job comes last.
func sortOpeners(st *PageState, o []Control) {
	path := urlPath(st.URL)
	score := func(c Control) int {
		s := 0
		if c.Aside {
			s -= 10
		}
		if c.Tag == "button" || c.Tag == "input" {
			s += 4
		}
		if c.Href != "" {
			h := strings.ToLower(c.Href)
			switch {
			case strings.Contains(h, "apply") || strings.Contains(h, "application"):
				s += 3
			case path != "" && strings.Contains(h, path):
				s += 2
			case strings.Contains(h, "/job"):
				s -= 3
			}
		}
		return s
	}
	sort.SliceStable(o, func(i, j int) bool { return score(o[i]) > score(o[j]) })
}

func urlPath(raw string) string {
	u := strings.SplitN(raw, "://", 2)
	rest := raw
	if len(u) == 2 {
		rest = u[1]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		p := rest[i:]
		if j := strings.IndexAny(p, "?#"); j >= 0 {
			p = p[:j]
		}
		return strings.ToLower(strings.TrimRight(p, "/"))
	}
	return ""
}

// controlKey identifies a control across probes for the opener and stall
// bookkeeping.
// The role and tag tell a dropdown's toggle from the menu item it reveals when
// both read "Apply now".
func controlKey(c Control) string {
	return strings.ToLower(strings.Join(c.Labels, "|")) + "@" + c.Href + "#" + c.Role + ":" + c.Tag
}

// refindKey is controlKey with the link's address cut at its query and
// fragment: a careers page that rewrites its Apply link after load to carry
// the landing page's tracking parameters (Capital One on TalentBrew) still
// shows the same link.
func refindKey(c Control) string {
	c.Href, _, _ = strings.Cut(c.Href, "?")
	c.Href, _, _ = strings.Cut(c.Href, "#")
	return controlKey(c)
}

var resumeWordRE = lazyregexp.New(`(?i)resume|r[eé]sum[eé]|\bcv\b|curriculum|cover.?letter`)

// filterSelectRE reads the label of a posting page's own filter menu (Teamtailor's
// "Select location" beside the job's locations, Pinterest's bare "Location:"):
// a choice of where to look, not a question of the application.
var filterSelectRE = lazyregexp.New(`(?i)^\s*(?:(?:select|choose|filter by)\s+(?:a\s+|an\s+|the\s+)?)?(?:location|office|city|country|department|team|language)s?\s*:?\s*$`)

// roleOnlyLabelRE is a label that names only the control's role, or nothing.
var roleOnlyLabelRE = lazyregexp.New(`(?i)^\s*(?:combo ?box|select|list ?box|text ?box)?\s*$`)

// dropMatchWidget marks the one unnamed optional upload of a posting page, its
// one location filter menu outside any form, or its one optional control
// outside any form named only by its role, as furniture when the page
// offers an Apply opener: a match-score or resume-match widget or a location
// filter is not the application, and reading it as one hides the real Apply
// control. With no opener on the page the same upload is an application step
// (a signed-in resume step), so it stays.
func (s *PageState) dropMatchWidget() {
	only := -1
	for i, in := range s.Inputs {
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		if only >= 0 {
			return
		}
		only = i
	}
	if only < 0 {
		return
	}
	in := s.Inputs[only]
	upload := in.Type == "file" && !in.Required && !resumeWordRE.MatchString(in.Label+" "+in.Name+" "+in.ID)
	// A menu with no blank choice reads as required, so the filter is not held to that.
	filter := in.Form < 0 && in.Kind == "select" && (filterSelectRE.MatchString(in.Label) || in.Label == "" && filterSelectRE.MatchString(in.Name))
	// A control outside any form whose only name is its role (Rippling's
	// language picker, aria-label "combobox", in the page's bottom bar) asks
	// nothing.
	unnamed := in.Form < 0 && !in.Required && roleOnlyLabelRE.MatchString(in.Label)
	if !upload && !filter && !unnamed {
		return
	}
	probe := *s
	probe.Inputs = nil
	for _, c := range Categorize(&probe, false).Openers {
		if isApplyLabel(c.Labels) {
			s.Inputs[only].Furniture = true
			return
		}
	}
}

var (
	// profileDialogRE is the heading of the dialog that saves the account's
	// profile after sign-in (Eightfold: "Create your profile").
	profileDialogRE = lazyregexp.New(`(?i)^\s*create\s+(?:your|a)\s+(?:candidate\s+)?profile\s*$`)
	// phoneCodeRE is the country-code box beside a phone number.
	phoneCodeRE = lazyregexp.New(`(?i)^\s*(?:phone\s+)?(?:country|dial(?:ing)?)\s+code\s*\*?\s*$`)
)

// profileDialog reports a modal dialog that saves the account's profile and
// holds only identity boxes (a name, the email, the phone and its country
// code), every one filled: its Submit saves the profile, not the application.
func (st *PageState) profileDialog() bool {
	if !slices.ContainsFunc(st.Dialogs, func(d DialogInfo) bool { return profileDialogRE.MatchString(d.Heading) }) {
		return false
	}
	n := 0
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if !in.Modal || in.Type == "hidden" {
			continue
		}
		switch f, _ := identityFieldOf(in.Label); {
		case !in.Filled:
			return false
		case f == gates.FieldFirstName, f == gates.FieldLastName, f == gates.FieldFullName, f == gates.FieldEmail, f == gates.FieldPhone, phoneCodeRE.MatchString(in.Label):
			n++
		default:
			return false
		}
	}
	return n > 0
}

// walkStage is the stage the walk presses a navigation control as.
func (c Control) walkStage() gates.ControlStage {
	if c.ProfileStep {
		return gates.StageProfileDialog
	}
	return gates.StageWalk
}

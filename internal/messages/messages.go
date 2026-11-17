// Clean-room: docs/provenance.md#CR-MESSAGES

// Package messages drafts the short messages a job seeker sends by hand: a
// LinkedIn outreach note to a person at a company, and a follow-up on an
// application in flight. A draft rests only on the user's own material (CV,
// profile, tracker row, evaluation fit) and on what the user says about the
// contact or the conversation; every claim is listed as confirmed by a source
// or to be checked. Nothing is stored and nothing is sent.
package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/evaluate"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// Model is the part of the LLM router the drafts use. It names the task,
// never a provider.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
}

// Task is the routing task of every draft.
const Task = "message-draft"

// Contact kinds of an outreach note.
const (
	ContactRecruiter     = "recruiter"
	ContactHiringManager = "hiring_manager"
	ContactPeer          = "peer"
	ContactInterviewer   = "interviewer"
)

// ContactKinds lists the contact kinds.
var ContactKinds = []string{ContactRecruiter, ContactHiringManager, ContactPeer, ContactInterviewer}

// Follow-up kinds.
const (
	FollowupPing   = "recruiter_ping"
	FollowupStatus = "status_check"
	FollowupThanks = "thank_you"
)

// FollowupReply answers the newest message of Mail.
const FollowupReply = "reply"

// FollowupKinds lists the follow-up kinds.
var FollowupKinds = []string{FollowupPing, FollowupStatus, FollowupThanks}

// Character ceilings of one message: a LinkedIn connection request holds 300.
const (
	OutreachMaxChars = 300
	FollowupMaxChars = 600
)

var (
	// ErrInvalid is a request the drafts cannot work from.
	ErrInvalid = errors.New("messages: invalid request")
	// ErrNotFound is an unknown tracker row.
	ErrNotFound = errors.New("messages: not found")
	// ErrCadence is a follow-up the tracker's cadence advises against.
	ErrCadence = errors.New("messages: follow-up cadence")
	// ErrNoDraft is a reply none of whose messages passed the checks.
	ErrNoDraft = errors.New("messages: no message passed the checks")
)

// Deps is everything a Service needs. Policy is optional: without it the
// follow-up cadence is read from the workspace's policy.yml.
type Deps struct {
	Store     *store.Store
	Tracker   *tracker.Tracker
	Workspace *workspace.Workspace
	Model     Model
	Prompts   *prompts.Set
	Policy    func() workspace.Followup
	Now       func() time.Time
}

// Service drafts messages for one workspace.
type Service struct{ d Deps }

// New returns a Service.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Tracker == nil || d.Workspace == nil || d.Model == nil {
		return nil, errors.New("messages: a store, tracker, workspace and model are required")
	}
	if d.Prompts == nil {
		d.Prompts = prompts.New("")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Policy == nil {
		ws := d.Workspace
		d.Policy = func() workspace.Followup { pol, _ := ws.FollowupPolicy(); return pol }
	}
	return &Service{d: d}, nil
}

// Contact is the person an outreach note goes to, as the user describes them.
type Contact struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Note is what the user knows about the person (their work, a talk, a
	// post); it is the only source about them.
	Note string `json:"note,omitempty"`
	// Date is the meeting date for an interviewer.
	Date string `json:"date,omitempty"`
}

// OutreachRequest names the application, or the company and role when there
// is no tracker row yet, and the contact.
type OutreachRequest struct {
	Candidacy int64
	Company   string
	Role      string
	Contact   Contact
}

// FollowupRequest names the application. An empty Kind is read from the row's
// status and cadence. Context is what the user remembers of the last
// exchange, in their words.
type FollowupRequest struct {
	Candidacy int64
	Kind      string
	Contact   string
	Context   string
	// Mail is the linked email thread, scrubbed: facts and the last exchange.
	Mail string
}

// Variant is one message and the angle it takes.
type Variant struct {
	Angle   string `json:"angle"`
	Message string `json:"message"`
}

// Draft is a checked draft. Confirmed lists the claims that rest on the
// user's own material, ToConfirm what the user must check or fill before
// sending, and Dropped the messages the checks removed and why.
type Draft struct {
	Mode         string       `json:"mode"`
	Kind         string       `json:"kind"`
	Candidacy    int64        `json:"candidacy,omitempty"`
	Company      string       `json:"company"`
	Role         string       `json:"role"`
	Primary      Variant      `json:"primary"`
	Alternatives []Variant    `json:"alternatives"`
	Confirmed    []string     `json:"confirmed"`
	ToConfirm    []string     `json:"to_confirm"`
	Dropped      []string     `json:"dropped"`
	Notes        []string     `json:"notes"`
	Prompt       prompts.Info `json:"prompt"`
}

// Text renders a draft for the terminal and the Ask Agent: the message, the
// alternatives, the checklist and the notes.
func (d Draft) Text() string {
	var b strings.Builder
	what := "Outreach draft"
	if d.Mode == "followup" {
		what = "Follow-up draft"
	}
	fmt.Fprintf(&b, "%s (%s) for %s", what, d.Kind, d.Company)
	if d.Role != "" {
		fmt.Fprintf(&b, ", %s", d.Role)
	}
	b.WriteString(".\n\n")
	variant := func(title string, v Variant) {
		if v.Angle != "" {
			title += " (" + v.Angle + ")"
		}
		fmt.Fprintf(&b, "%s:\n%s\n\n", title, v.Message)
	}
	variant("Message", d.Primary)
	for i, v := range d.Alternatives {
		variant(fmt.Sprintf("Alternative %d", i+1), v)
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString(title + ":\n")
		for _, it := range items {
			b.WriteString("- " + it + "\n")
		}
		b.WriteString("\n")
	}
	list("Rests on your own material", d.Confirmed)
	list("Check before sending", d.ToConfirm)
	list("Removed by the checks", d.Dropped)
	for _, n := range d.Notes {
		b.WriteString(n + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

const system = "You draft short messages a job seeker sends by hand. Reply with the JSON the user asks for and nothing else."

// reply is the structured answer of the message-draft task.
type reply struct {
	Primary      Variant   `json:"primary"`
	Alternatives []Variant `json:"alternatives"`
	Claims       []struct {
		Text  string   `json:"text"`
		Facts []string `json:"facts"`
	} `json:"claims"`
	Assumptions []string `json:"assumptions"`
}

func schema(minAlt int) json.RawMessage {
	variant := `{"type":"object","additionalProperties":false,"required":["angle","message"],"properties":{"angle":{"type":"string","maxLength":120},"message":{"type":"string","minLength":1,"maxLength":2000}}}`
	claim := `{"type":"object","additionalProperties":false,"required":["text","facts"],"properties":{"text":{"type":"string","minLength":1,"maxLength":400},"facts":{"type":"array","maxItems":6,"items":{"type":"string","maxLength":12}}}}`
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["primary","alternatives","claims","assumptions"],"properties":{` +
		`"primary":` + variant +
		`,"alternatives":{"type":"array","minItems":` + strconv.Itoa(minAlt) + `,"maxItems":2,"items":` + variant + `}` +
		`,"claims":{"type":"array","maxItems":20,"items":` + claim + `}` +
		`,"assumptions":{"type":"array","maxItems":10,"items":{"type":"string","maxLength":300}}}}`)
}

type roleView struct{ Company, Title string }

type contactView struct{ Kind, Name, Note, Date string }

type messageView struct{ Kind, Note string }

type outreachData struct {
	Contact contactView
	Role    roleView
	Facts   []factView
	Voice   string
}

type followupData struct {
	Message messageView
	Contact contactView
	Role    roleView
	Facts   []factView
	Voice   string
}

// Outreach drafts a three-sentence note to one contact, with two alternative
// angles.
func (s *Service) Outreach(ctx context.Context, req OutreachRequest) (Draft, error) {
	if !slices.Contains(ContactKinds, req.Contact.Kind) {
		return Draft{}, fmt.Errorf("%w: the contact kind must be one of %s", ErrInvalid, strings.Join(ContactKinds, ", "))
	}
	d := Draft{Mode: "outreach", Kind: req.Contact.Kind, Candidacy: req.Candidacy, Company: strings.TrimSpace(req.Company), Role: strings.TrimSpace(req.Role)}
	b := &factBook{}
	if req.Candidacy != 0 {
		a, err := s.application(ctx, req.Candidacy)
		if err != nil {
			return Draft{}, err
		}
		d.Company, d.Role = a.entry.Company, a.entry.Role
		b.trackerFacts(a, s.d.Now().Location())
		s.reportFacts(ctx, b, a.entry)
	}
	if d.Company == "" {
		return Draft{}, fmt.Errorf("%w: name the application or the company", ErrInvalid)
	}
	prof, err := evaluate.LoadProfile(s.d.Workspace)
	if err != nil {
		return Draft{}, err
	}
	b.profileFacts(prof)
	note := clip(req.Contact.Note, maxNoteChars)
	if note != "" {
		b.add("U", SourceUser, "About the contact: "+note)
	}
	data := outreachData{
		Contact: contactView{Kind: req.Contact.Kind, Name: prompts.Escape(clip(req.Contact.Name, 120)), Note: prompts.Escape(cmpOr(note, "nothing")), Date: prompts.Escape(clip(req.Contact.Date, 40))},
		Role:    roleView{Company: prompts.Escape(d.Company), Title: prompts.Escape(d.Role)},
		Facts:   b.view(), Voice: prompts.Escape(s.voice()),
	}
	user := []string{d.Company, d.Role, req.Contact.Name, note, req.Contact.Date}
	if err := s.draft(ctx, &d, "outreach", data, 2, OutreachMaxChars, b, user); err != nil {
		return Draft{}, err
	}
	d.Notes = append(d.Notes, "Nothing was sent. Copy the message you choose and send it yourself.")
	if req.Contact.Name == "" {
		d.ToConfirm = append(d.ToConfirm, "Add the contact's name before sending.")
	}
	return d, nil
}

// Followup drafts one follow-up on an application in flight. The kind comes
// from the request or from the row: an applied row gets a recruiter ping, a
// responded row a status check, an interview row a thank-you.
func (s *Service) Followup(ctx context.Context, req FollowupRequest) (Draft, error) {
	if req.Kind != "" && !slices.Contains(FollowupKinds, req.Kind) && (req.Kind != FollowupReply || req.Mail == "") {
		return Draft{}, fmt.Errorf("%w: the follow-up kind must be one of %s", ErrInvalid, strings.Join(FollowupKinds, ", "))
	}
	if req.Candidacy == 0 {
		return Draft{}, fmt.Errorf("%w: name the application", ErrInvalid)
	}
	a, err := s.application(ctx, req.Candidacy)
	if err != nil {
		return Draft{}, err
	}
	e := a.entry
	switch e.Status {
	case tracker.Applied, tracker.Responded, tracker.Interview, tracker.Offer:
	default:
		return Draft{}, fmt.Errorf("%w: application %d is %s, so there is nothing in flight to follow up", ErrInvalid, e.ID, e.Status)
	}
	if e.ArchivedAt != "" {
		return Draft{}, fmt.Errorf("%w: application %d is archived", ErrInvalid, e.ID)
	}
	d := Draft{Mode: "followup", Kind: req.Kind, Candidacy: e.ID, Company: e.Company, Role: e.Role}
	if d.Kind == "" {
		switch e.Status {
		case tracker.Applied:
			d.Kind = FollowupPing
		case tracker.Responded:
			d.Kind = FollowupStatus
		case tracker.Interview:
			d.Kind = FollowupThanks
		default:
			return Draft{}, fmt.Errorf("%w: say which follow-up to draft (%s)", ErrInvalid, strings.Join(FollowupKinds, ", "))
		}
	}
	if d.Kind == FollowupPing && e.Status == tracker.Applied {
		note, err := s.cadence(ctx, e.ID, req.Kind == "" && req.Mail == "")
		if err != nil {
			return Draft{}, err
		}
		if note != "" {
			d.Notes = append(d.Notes, note)
		}
	}
	b := &factBook{}
	b.trackerFacts(a, s.d.Now().Location())
	s.reportFacts(ctx, b, e)
	prof, err := evaluate.LoadProfile(s.d.Workspace)
	if err != nil {
		return Draft{}, err
	}
	b.profileFacts(prof)
	exchange := clip(req.Context, maxNoteChars)
	if exchange != "" {
		b.add("U", SourceUser, "From the last exchange: "+exchange)
	}
	if req.Mail != "" {
		for _, l := range strings.Split(req.Mail, "\n") {
			b.add("M", SourceMail, l)
		}
		// The newest mail is last.
		r := []rune(clip(req.Mail, 1<<20))
		exchange = string(r[max(0, len(r)-maxNoteChars):])
	}
	data := followupData{
		Message: messageView{Kind: d.Kind, Note: prompts.Escape(exchange)},
		Contact: contactView{Name: prompts.Escape(clip(req.Contact, 120))},
		Role:    roleView{Company: prompts.Escape(e.Company), Title: prompts.Escape(e.Role)},
		Facts:   b.view(), Voice: prompts.Escape(s.voice()),
	}
	user := []string{e.Company, e.Role, req.Contact, exchange}
	if err := s.draft(ctx, &d, "followup", data, 0, FollowupMaxChars, b, user); err != nil {
		return Draft{}, err
	}
	sent := "Nothing was sent. Copy the message and send it yourself."
	if e.Status == tracker.Applied {
		sent += fmt.Sprintf(" Once it is sent, record it (I sent this on the Tracker, or `applysmith tracker followup %d`) so the cadence counts it.", e.ID)
	}
	d.Notes = append(d.Notes, sent)
	return d, nil
}

// cadence reads the row's follow-up suggestion. A row whose cadence is used
// up is refused when the kind was inferred; a ping the user asked for by name
// is drafted with a note, because the user decides.
func (s *Service) cadence(ctx context.Context, id int64, inferred bool) (string, error) {
	list, err := s.d.Tracker.Followups(ctx, s.d.Policy(), false)
	if err != nil {
		return "", err
	}
	today := s.d.Now().Format("2006-01-02")
	for _, sg := range list {
		if sg.ID != id {
			continue
		}
		switch sg.Kind {
		case tracker.KindFollowUp:
			if sg.Due > today {
				return fmt.Sprintf("Follow-up %d is not due until %s; the cadence suggests waiting.", sg.Number, sg.Due), nil
			}
			return fmt.Sprintf("Follow-up %d has been due since %s.", sg.Number, sg.Due), nil
		case tracker.KindCadenceExhausted, tracker.KindConsiderClosing:
			if inferred {
				return "", fmt.Errorf("%w: the follow-ups the cadence allows are already recorded for application %d with no reply; consider closing it, or ask for a recruiter_ping by name", ErrCadence, id)
			}
			return "The follow-ups the cadence allows are already recorded; a further ping is your call.", nil
		}
	}
	return "", nil
}

func cmpOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// draft renders the template, calls the model and applies the checks.
func (s *Service) draft(ctx context.Context, d *Draft, name string, data any, minAlt, maxChars int, b *factBook, user []string) error {
	prompt, info, err := s.d.Prompts.Render(name, data)
	if err != nil {
		return err
	}
	d.Prompt = info
	if d.Notes == nil {
		d.Notes = []string{}
	}
	deadline, _ := ctx.Deadline()
	var out reply
	if _, err := s.d.Model.Structured(ctx, llm.Request{Task: Task, System: system, Prompt: prompt, Deadline: deadline}, schema(minAlt), &out); err != nil {
		return err
	}
	grounded := numbersIn(b.list, user)
	var kept []Variant
	d.Dropped = []string{}
	for i, v := range append([]Variant{out.Primary}, out.Alternatives...) {
		v.Message, v.Angle = strings.TrimSpace(v.Message), clip(v.Angle, 120)
		if why := check(v.Message, maxChars, grounded); why != "" {
			label := "the primary message"
			if i > 0 {
				label = fmt.Sprintf("alternative %d", i)
			}
			d.Dropped = append(d.Dropped, label+": "+why)
			continue
		}
		if i > 0 && len(kept) == 0 {
			d.Notes = append(d.Notes, "The primary message failed the checks, so the first alternative that passed is shown instead.")
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		return fmt.Errorf("%w: %s", ErrNoDraft, strings.Join(d.Dropped, "; "))
	}
	d.Primary, d.Alternatives = kept[0], append([]Variant{}, kept[1:]...)
	d.Confirmed, d.ToConfirm = checklist(out, b.byID())
	if s.voice() != "" {
		d.Notes = append(d.Notes, "Written toward your voice notes (Profile, Voice).")
	} else {
		d.Notes = append(d.Notes, "No voice notes yet: add them under Profile, Voice so drafts sound like you.")
	}
	return nil
}

// checklist sorts the model's claims: a claim whose every cited id is a fact
// that was given is confirmed by that material; anything else, and every
// assumption, is for the user to check before sending.
func checklist(out reply, facts map[string]Fact) (confirmed, toConfirm []string) {
	confirmed, toConfirm = []string{}, []string{}
	for _, c := range out.Claims {
		text := clip(c.Text, 400)
		if text == "" {
			continue
		}
		var cites []string
		why := ""
		if len(c.Facts) == 0 {
			why = "no source in your material"
		}
		for _, id := range c.Facts {
			f, ok := facts[strings.TrimSpace(id)]
			if !ok {
				why = "cites a source that was not given"
				break
			}
			cites = append(cites, sourceNames[f.Source]+": \""+clipTo(f.Text, 70)+"\"")
		}
		if why == "" {
			confirmed = append(confirmed, fmt.Sprintf("%s (%s)", text, strings.Join(cites, "; ")))
		} else {
			toConfirm = append(toConfirm, text+" ("+why+")")
		}
	}
	for _, a := range out.Assumptions {
		if a = clip(a, 300); a != "" {
			toConfirm = append(toConfirm, a)
		}
	}
	return confirmed, toConfirm
}

var (
	numberShape = lazyregexp.New(`\d+(?:[.,]\d+)*`)
	stockPhrase = lazyregexp.New(`(?i)\b(passionate about|results[- ]oriented|circl(e|ing) back|just checking in|touch(ing)? base)\b`)
)

// numbersIn collects every number the sources state, without separators.
func numbersIn(facts []Fact, user []string) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		for _, n := range numberShape.FindAllString(s, -1) {
			out[strings.ReplaceAll(n, ",", "")] = true
		}
	}
	for _, f := range facts {
		add(f.Text)
	}
	for _, u := range user {
		add(u)
	}
	return out
}

// check returns why a message fails, or the empty string. It fails when it is
// empty or too long, holds a phone number, an email address or a link, uses a
// stock phrase, or states a number no source states.
func check(msg string, maxChars int, grounded map[string]bool) string {
	switch {
	case msg == "":
		return "it is empty"
	case len([]rune(msg)) > maxChars:
		return fmt.Sprintf("it has %d characters, over the limit of %d", len([]rune(msg)), maxChars)
	case contactShaped(msg):
		return "it holds a phone number, an email address or a link"
	}
	if m := stockPhrase.FindString(msg); m != "" {
		return fmt.Sprintf("it uses the stock phrase %q", m)
	}
	for _, n := range numberShape.FindAllString(msg, -1) {
		if !grounded[strings.ReplaceAll(n, ",", "")] {
			return fmt.Sprintf("it states the number %s, which no source states", n)
		}
	}
	return ""
}

// Fact is one numbered line of source material a draft may rest on.
type Fact struct {
	ID     string
	Source string
	Text   string
}

// sourceNames says in words where a fact came from, for the checklist.
var sourceNames = map[string]string{SourceCV: "your CV", SourceProfile: "your profile", SourceTracker: "the tracker", SourceEvaluation: "the evaluation", SourceUser: "what you wrote", SourceMail: "the email thread"}

// clipTo shortens a quoted fact, marking the cut.
func clipTo(s string, n int) string {
	if c := clip(s, n); c != s {
		return c + "..."
	}
	return s
}

// Fact sources.
const (
	SourceCV         = "cv"
	SourceProfile    = "profile"
	SourceTracker    = "tracker"
	SourceEvaluation = "evaluation"
	SourceUser       = "you"
	SourceMail       = "mail"
)

const (
	maxCVFacts     = 40
	maxReportFacts = 6
	maxFactChars   = 300
	maxNoteChars   = 1500
	maxVoiceChars  = 1500
)

var (
	phoneShape = lazyregexp.New(`\+?\d[\d\s().-]{6,}\d`)
	emailShape = lazyregexp.New(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	linkShape  = lazyregexp.New(`(?i)https?://|www\.`)
	bulletMark = lazyregexp.New(`^\s*(?:[-*+\x{2022}]|\d+[.)])\s+`)
)

// contactShaped reports text that holds a phone number, an email address or a
// link. A run of digits counts as a phone number from nine digits on, so a
// date does not.
func contactShaped(s string) bool {
	if emailShape.MatchString(s) || linkShape.MatchString(s) {
		return true
	}
	for _, m := range phoneShape.FindAllString(s, -1) {
		if digits(m) >= 9 {
			return true
		}
	}
	return false
}

func digits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

type factBook struct {
	list  []Fact
	count map[string]int
}

func (b *factBook) add(prefix, source, text string) {
	text = clip(text, maxFactChars)
	if text == "" {
		return
	}
	if b.count == nil {
		b.count = map[string]int{}
	}
	b.count[prefix]++
	b.list = append(b.list, Fact{ID: fmt.Sprintf("%s%d", prefix, b.count[prefix]), Source: source, Text: text})
}

// profileFacts adds the CV lines and the profile's history, skills and
// targets. Identity, contact and work-authorization details are never facts:
// a CV line holding a phone number, an email address or a link is skipped.
func (b *factBook) profileFacts(p evaluate.Profile) {
	n := 0
	for _, raw := range strings.Split(p.CV, "\n") {
		line := strings.TrimSpace(bulletMark.ReplaceAllString(raw, ""))
		if strings.HasPrefix(line, "#") || len(strings.Fields(line)) < 4 || contactShaped(line) {
			continue
		}
		if n++; n > maxCVFacts {
			break
		}
		b.add("F", SourceCV, line)
	}
	for _, e := range p.History.Employment {
		end := e.End
		if end == "" {
			end = "present"
		}
		b.add("F", SourceProfile, fmt.Sprintf("%s at %s, %s to %s", e.Title, e.Employer, e.Start, end))
	}
	for _, e := range p.History.Education {
		b.add("F", SourceProfile, strings.TrimSpace(fmt.Sprintf("%s %s, %s", e.Degree, e.Field, e.School)))
	}
	var skills []string
	for _, s := range p.Skills {
		skills = append(skills, s.Name)
	}
	if len(skills) > 0 {
		b.add("F", SourceProfile, "Skills: "+strings.Join(skills, ", "))
	}
	if len(p.Targets.Locations) > 0 {
		b.add("F", SourceProfile, "Open to working in: "+strings.Join(p.Targets.Locations, "; "))
	}
	if len(p.Preferences.WorkModes) > 0 {
		b.add("F", SourceProfile, "Work modes: "+strings.Join(p.Preferences.WorkModes, ", "))
	}
}

// application is the tracker row a draft is about, read once.
type application struct {
	entry  tracker.Entry
	events []tracker.Event
}

func (s *Service) application(ctx context.Context, id int64) (application, error) {
	det, err := s.d.Tracker.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return application{}, fmt.Errorf("%w: application %d", ErrNotFound, id)
	}
	if err != nil {
		return application{}, err
	}
	return application{entry: det.Entry, events: det.Events}, nil
}

// day is the calendar day of a tracker stamp where the user is, the day the
// tracker's cadence counts from. An applied date is a day already.
func day(at string, loc *time.Location) string {
	if ts, err := time.Parse(time.RFC3339, at); err == nil {
		return ts.In(loc).Format("2006-01-02")
	}
	if len(at) >= 10 {
		return at[:10]
	}
	return at
}

// trackerFacts adds what the tracker row records: when the user applied, the
// current status and since when, and the follow-ups already recorded and not
// undone.
func (b *factBook) trackerFacts(a application, loc *time.Location) {
	e := a.entry
	if e.AppliedAt != "" {
		b.add("T", SourceTracker, fmt.Sprintf("Applied for %s at %s on %s", e.Role, e.Company, day(e.AppliedAt, loc)))
	}
	since := ""
	var followups []string
	undone := map[int64]bool{}
	for _, ev := range a.events {
		if n := ev.Undoes(); n != 0 {
			undone[n], undone[ev.ID] = true, true
		}
	}
	for _, ev := range a.events {
		switch ev.Kind {
		case tracker.EventStatusChanged, tracker.EventStatusCorrected, tracker.EventCreated:
			if ev.NewStatus == e.Status {
				since = day(ev.At, loc)
			}
		case tracker.EventFollowupRecorded:
			if !undone[ev.ID] {
				followups = append(followups, day(ev.At, loc))
			}
		}
	}
	status := "Status: " + string(e.Status)
	if since != "" {
		status += " since " + since
	}
	b.add("T", SourceTracker, status)
	if len(followups) > 0 {
		b.add("T", SourceTracker, fmt.Sprintf("Follow-ups already sent: %d, the last on %s", len(followups), followups[len(followups)-1]))
	}
}

// reportFacts adds the requirements of the row's evaluation that the profile
// meets, each with its evidence. The level, pay and negotiation sections of a
// report never reach a message.
func (s *Service) reportFacts(ctx context.Context, b *factBook, e tracker.Entry) {
	if e.ReportNumber == nil {
		return
	}
	ref, err := evaluate.FindReport(ctx, s.d.Store, s.d.Workspace.Root, *e.ReportNumber)
	if err != nil {
		return
	}
	md, err := ref.Markdown()
	if err != nil {
		return
	}
	for i, it := range evaluate.ReportMatches(string(md)) {
		if i == maxReportFacts {
			break
		}
		b.add("R", SourceEvaluation, fmt.Sprintf("The posting asks for %s; the profile shows: %s", it.Label, it.Evidence))
	}
}

// voice is the user's style notes, if the workspace has them.
func (s *Service) voice() string {
	c, err := s.d.Workspace.Read(workspace.FileVoice)
	if err != nil || c.Revision == workspace.RevisionAbsent {
		return ""
	}
	return clip(string(c.Data), maxVoiceChars)
}

// factView is a fact as the template reads it, escaped.
type factView struct{ ID, Source, Text string }

func (b *factBook) view() []factView {
	out := make([]factView, len(b.list))
	for i, f := range b.list {
		out[i] = factView{ID: f.ID, Source: f.Source, Text: prompts.Escape(f.Text)}
	}
	return out
}

func (b *factBook) byID() map[string]Fact {
	m := make(map[string]Fact, len(b.list))
	for _, f := range b.list {
		m[f.ID] = f
	}
	return m
}

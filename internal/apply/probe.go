package apply

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

//go:embed probe.js
var probeJS string

// PageState is one read-only look at the document.
type PageState struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Text     string   `json:"text"`
	Lines    []string `json:"lines"`
	Headings []string `json:"headings"`
	// Forms are the visible forms with the counts the inventory fills in.
	Forms []FormInfo `json:"forms"`
	// Inputs is the walker's field inventory. It never comes from the page
	// probe: Probe fills it from the engine's inspect result alone.
	Inputs   []InputInfo `json:"-"`
	Controls []Control   `json:"controls"`
	Captcha  []CaptchaW  `json:"captcha"`
	Resp     []RespField `json:"respFields"`
	Loading  bool        `json:"loading"`
	// ActiveStep is the step Workday's progress bar marks active ("current
	// step 3 of 5 Application Questions"), nil when the page has no bar.
	ActiveStep *string `json:"activeStep"`
	// FlowLoading is true while a step of Workday's apply flow is still its
	// loading page or its "Loading" skeleton, or a busy layer covers the
	// window, over fields that are not the new step's.
	FlowLoading bool        `json:"flowLoading"`
	Frames      []FrameInfo `json:"frames"`
	// Links are the visible http(s) links of the page, in document order.
	Links []LinkInfo `json:"links"`
	// GreenhouseBoard is the board a Greenhouse embed script of the document
	// itself names (embed/job_board/js?for=<board>), empty when there is none.
	GreenhouseBoard string    `json:"greenhouseBoard"`
	Step            *StepInfo `json:"step"`
	// Validation holds the page's own validation words.
	Validation []string `json:"validation"`
	// Stall is what the page shows of a refusal it did not mark; it explains a
	// stalled walk and is never the refusal reader's input.
	Stall StallHints `json:"stall"`
	Modal bool       `json:"modal"`
	// Oracle is set on a page with the timeline add buttons of Oracle
	// Recruiting Cloud's Work and Education History sections.
	Oracle *OracleSections `json:"oracle"`
	// Dialogs are the visible dialogs and code-shaped forms, with the handles
	// of their boxes, options and buttons.
	Dialogs []DialogInfo `json:"dialogs"`
	// Account is what the site's header says of the candidate's session
	// (Workday's utility bar): accountSignedIn, accountSignedOut, or empty
	// where the page says nothing.
	Account string `json:"account"`
}

// The header readings of PageState.Account.
const (
	accountSignedIn  = "signed-in"
	accountSignedOut = "signed-out"
)

// StallHints are short red-classed validation sentences the page shows and,
// from the inventory, the labels of the visible required boxes still empty.
type StallHints struct {
	Messages []string `json:"messages"`
	Empty    []string `json:"-"`
}

// DialogInfo is one visible dialog (or a form of four to eight boxes).
type DialogInfo struct {
	Heading string         `json:"heading"`
	Text    string         `json:"text"`
	Form    bool           `json:"form"`
	Blocked bool           `json:"blocked"`
	Boxes   []DialogBox    `json:"boxes"`
	Options []DialogOption `json:"options"`
	Buttons []DialogButton `json:"buttons"`
	// Closer is the handle of the dialog's own close control, nil when none.
	Closer *int `json:"closer"`
}

// DialogBox is a visible text box of a dialog.
type DialogBox struct {
	I         int    `json:"i"`
	Label     string `json:"label"`
	MaxLength int    `json:"maxLength"`
}

// DialogOption is a radio-like choice of a dialog.
type DialogOption struct {
	I     int    `json:"i"`
	Value string `json:"value"`
	Label string `json:"label"`
}

// DialogButton is a button of a dialog.
type DialogButton struct {
	I        int    `json:"i"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// OracleSections are the timeline add buttons and an open editor's footer
// buttons (probe handles, -1 when absent) and the text of the entries already
// on each timeline.
type OracleSections struct {
	AddExperience int      `json:"addExperience"`
	AddEducation  int      `json:"addEducation"`
	Save          int      `json:"save"`
	Cancel        int      `json:"cancel"`
	Experience    []string `json:"experience"`
	Education     []string `json:"education"`
}

// FormInfo describes one visible form: the page's facts about it, and the
// counts and verdict the inventory adds.
type FormInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Role   string `json:"role"`
	Cls    string `json:"cls"`
	Text   string `json:"text"`
	// TextLen is the length of the form's whole text; Text is its start.
	TextLen int `json:"textLen"`

	Fields    int  `json:"-"`
	Files     int  `json:"-"`
	HasName   bool `json:"-"`
	HasResume bool `json:"-"`
	Furniture bool `json:"-"`
}

// InputInfo is one control of the engine's inspect result, with the handle
// and page context the stages need; I is its handle for focus.
type InputInfo struct {
	I            int
	Selector     string
	Kind         string
	Tag          string
	Type         string
	Name         string
	ID           string
	Label        string
	Autocomplete string
	MaxLength    int
	Placeholder  string
	Required     bool
	Form         int
	Modal        bool
	Furniture    bool
	Disabled     bool
	Checked      bool
	Filled       bool
	// InspectionOnly marks an authentication box or an attachment slot: its
	// structure is kept, its value never read and it is never sent to the bank.
	InspectionOnly bool
	// ParserOnly marks an upload that only feeds the page's own parser; it is
	// not an attachment of the application.
	ParserOnly bool
	// Options are the visible texts of a select's options.
	Options []string
}

// Control is one visible control that can be pressed; I is its handle.
type Control struct {
	GoogleAccount string   `json:"googleAccount,omitempty"`
	I             int      `json:"i"`
	Role          string   `json:"role"`
	Tag           string   `json:"tag"`
	Labels        []string `json:"labels"`
	Type          string   `json:"type"`
	Disabled      bool     `json:"disabled"`
	Href          string   `json:"href"`
	Target        string   `json:"target"`
	Form          int      `json:"form"`
	Modal         bool     `json:"modal"`
	Chrome        bool     `json:"chrome"`
	Chat          bool     `json:"chat"`
	X             float64  `json:"x"`
	Y             float64  `json:"y"`
	Order         int      `json:"order"`
	Pressed       string   `json:"pressed"`
	// Selected is a tab's aria-selected.
	Selected string `json:"selected"`
	// Name is the control's name attribute.
	Name string `json:"name"`
	// ID is the control's id attribute.
	ID string `json:"id"`
	// Aside marks a control inside an <aside> rail; Card numbers the job card
	// (article or list item) it sits in from 1, 0 when none.
	Aside bool `json:"aside"`
	Card  int  `json:"card"`
	// FinalApply is set by Categorize, never by the page: the control is the
	// filled form's own final control and reads Apply instead of Submit.
	FinalApply bool `json:"-"`
	// ProfileStep is set by Categorize, never by the page: the control is the
	// Submit of a profile dialog (profileDialog), pressed as a walk step.
	ProfileStep bool `json:"-"`
}

// CaptchaW is one captcha widget the page shows.
type CaptchaW struct {
	Kind       string  `json:"kind"`
	SiteKey    string  `json:"siteKey"`
	Visible    bool    `json:"visible"`
	Invisible  bool    `json:"invisible"`
	W          float64 `json:"w"`
	H          float64 `json:"h"`
	BframeOpen bool    `json:"bframeOpen"`
}

// RespField is a captcha response field and whether it is still empty.
type RespField struct {
	Name  string `json:"name"`
	Empty bool   `json:"empty"`
}

// FrameInfo is an iframe of the page.
type FrameInfo struct {
	Src    string `json:"src"`
	Srcdoc bool   `json:"srcdoc"`
	// SrcdocText is the start of the frame's inline document.
	SrcdocText string  `json:"srcdocText"`
	Visible    bool    `json:"visible"`
	W          float64 `json:"w"`
	H          float64 `json:"h"`
}

// LinkInfo is one link: its text and absolute address.
type LinkInfo struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// StepInfo is the wizard's own step marker.
type StepInfo struct {
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Marker  string `json:"marker"`
}

// Last reports whether the marker says this is the last step.
func (s *StepInfo) Last() bool { return s != nil && s.Total > 0 && s.Current >= s.Total }

// Located is where a control stands at press time.
type Located struct {
	Found    bool     `json:"found"`
	Labels   []string `json:"labels"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	W        float64  `json:"w"`
	H        float64  `json:"h"`
	Hit      bool     `json:"hit"`
	Blocker  string   `json:"blocker"`
	Disabled bool     `json:"disabled"`
	Visible  bool     `json:"visible"`
	// InView is true when the control lies inside the viewport of its document.
	InView bool `json:"inView"`
	// AtLabels are the labels of what a press at the point presses besides the
	// control: the element there and its pressable ancestors up to the
	// control. Submitter is set when the control or one of them submits its
	// form, Link when one follows a link out of the page.
	AtLabels  []string `json:"atLabels"`
	Submitter bool     `json:"submitter"`
	Link      bool     `json:"link"`
}

func probeExpr(cmd string, arg any) string {
	a, _ := json.Marshal(arg)
	c, _ := json.Marshal(cmd)
	return "(" + strings.TrimSpace(probeJS) + ")(" + string(c) + "," + string(a) + ")"
}

// PrePress is the last look before the final press: Workday's Review recap
// items that read "No Response" and the required Yes/No groups that show no
// answer, by their labels.
type PrePress struct {
	NoResponse []string `json:"noResponse"`
	Toggles    []string `json:"toggles"`
}

// ReadPrePress reads the page's PrePress scans.
func ReadPrePress(ctx context.Context, t Tab) (PrePress, error) {
	var p PrePress
	err := t.Eval(ctx, probeExpr("prepress", nil), &p)
	return p, err
}

// Probe reads the document.
func Probe(ctx context.Context, t Tab) (*PageState, error) {
	var st PageState
	if err := t.Eval(ctx, probeExpr("probe", nil), &st); err != nil {
		return nil, err
	}
	if err := st.readInventory(ctx, t, ""); err != nil {
		return nil, err
	}
	st.dropMatchWidget()
	if st.HasForm() || len(st.Frames) == 0 {
		return &st, nil
	}
	return embedFrames(ctx, t, &st), nil
}

// Locate finds a probed control again, scrolled into view, with what it reads
// right now and whether anything covers its centre.
func Locate(ctx context.Context, t Tab, i int) (Located, error) {
	if i >= frameBase {
		return locateInFrame(ctx, t, i)
	}
	var l Located
	if err := t.Eval(ctx, probeExpr("locate", i), &l); err != nil {
		return l, err
	}
	return l, nil
}

// ConsentGate is a cookie notice, preference center or privacy-notice dialog
// the page shows, with the visible buttons of everything that reads as one.
type ConsentGate struct {
	Found bool   `json:"found"`
	Text  string `json:"text"`
	// Preference is set for a preference center; AllOff when every optional
	// category stands off; Asks when a box wanted a password or a signature,
	// which makes it no consent gate.
	Preference bool            `json:"preference"`
	AllOff     bool            `json:"allOff"`
	Asks       bool            `json:"asks"`
	Controls   []ConsentButton `json:"controls"`
}

// ConsentButton is a button of a consent gate; I is its handle.
type ConsentButton struct {
	I     int    `json:"i"`
	Label string `json:"label"`
}

// ReadConsent reads the consent gates standing on the page, after the last
// probe, without pressing anything.
func ReadConsent(ctx context.Context, t Tab) (ConsentGate, error) {
	var g ConsentGate
	err := t.Eval(ctx, probeExpr("consent", nil), &g)
	return g, err
}

// Focus focuses a probed input.
func Focus(ctx context.Context, t Tab, i int) (bool, error) {
	var ok bool
	err := t.Eval(ctx, probeExpr("focus", i), &ok)
	return ok, err
}

// FocusSecret focuses the box behind handle i only when it is a password box,
// the one kind of box a password is typed into.
func FocusSecret(ctx context.Context, t Tab, i int) (bool, error) {
	var ok bool
	err := t.Eval(ctx, probeExpr("focusSecret", i), &ok)
	return ok, err
}

// PageCompany is the employer the page itself states, empty when it states none.
func PageCompany(ctx context.Context, t Tab) string {
	var c string
	_ = t.Eval(ctx, probeExpr("company", nil), &c)
	return strings.TrimSpace(c)
}

// Stated is what a posting page's own JobPosting data names the posting: the
// company that hires, the role and where the job is. Each is text from a page,
// kept as one line without markup. The names are read by no gate or guard; the
// location is the posting's own wherever no board states one (postingLoc).
type Stated struct{ Company, Role, Location string }

// The most a stated text keeps; the board's title holds 120 characters.
const (
	maxStatedCompany  = 80
	maxStatedRole     = 120
	maxStatedLocation = 120
)

// PageStated reads what the page's JobPosting structured data says of its
// posting, from the page the tab shows and with no request of its own. A page
// that states several postings, or none, states nothing; neither does an answer
// that is not the strings asked for.
func PageStated(ctx context.Context, t Tab) Stated {
	var raw struct{ Title, Company, Location string }
	if err := t.Eval(ctx, probeExpr("posting", nil), &raw); err != nil {
		return Stated{}
	}
	return Stated{Company: pageName(raw.Company, maxStatedCompany), Role: pageName(raw.Title, maxStatedRole), Location: pageName(raw.Location, maxStatedLocation)}
}

// ChooseOption selects a native select's option by index.
func ChooseOption(ctx context.Context, t Tab, i, index int) (bool, error) {
	var ok bool
	err := t.Eval(ctx, probeExpr("choose", map[string]int{"i": i, "index": index}), &ok)
	return ok, err
}

// Choice is one member of a radio group: the name a person reads, whether it
// is chosen, and the handle a press uses.
type Choice struct {
	I       int    `json:"i"`
	Name    string `json:"name"`
	Checked bool   `json:"checked"`
}

// Choices reads the members of the radio group a selector names.
func Choices(ctx context.Context, t Tab, sel string) ([]Choice, error) {
	var out []Choice
	err := t.Eval(ctx, probeExpr("choices", sel), &out)
	return out, err
}

// TypeKeys types text into a probed box key by key with keydown, input and
// keyup events and reports whether the box holds it afterwards.
func TypeKeys(ctx context.Context, t Tab, i int, text string) (bool, error) {
	var ok bool
	err := t.Eval(ctx, probeExpr("typekeys", map[string]any{"i": i, "text": text}), &ok)
	return ok, err
}

// Signature is a cheap fingerprint of the page, to see whether a press moved
// it.
func Signature(ctx context.Context, t Tab) (string, error) {
	var s string
	err := t.Eval(ctx, probeExpr("signature", nil), &s)
	if err != nil {
		return s, err
	}
	// The fields are the engine's: its inspect result of the top document. An
	// empty one means the form, if there is one, lives in a sub-frame.
	rows, err := t.Page().Inspect(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(s)
	for _, r := range rows {
		b.WriteString("|" + r.Selector + r.Label)
	}
	if len(rows) == 0 {
		fs, err := frameSignature(ctx, t)
		if err != nil {
			return "", err
		}
		b.WriteString(fs)
	}
	return b.String(), nil
}

// HasForm reports whether the page holds a form worth filling: a visible
// field or file input outside furniture.
func (s *PageState) HasForm() bool {
	for _, in := range s.Inputs {
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		return true
	}
	return false
}

// FormFields counts the fillable fields outside furniture. An upload that only
// feeds the page's parser is a field, not an attachment.
func (s *PageState) FormFields() (fields, files int) {
	for _, in := range s.Inputs {
		if in.Furniture {
			continue
		}
		if in.Type == "file" && !in.ParserOnly {
			files++
		} else {
			fields++
		}
	}
	return
}

func (c Control) String() string { return fmt.Sprintf("%s %q", c.Tag, c.Labels) }

// Fingerprint is a cheap identity of what the page shows: its address, its
// input names, its control labels and the length of its text. Two probes of an
// unchanged page share it.
func (s *PageState) Fingerprint() string {
	var b strings.Builder
	b.WriteString(s.URL)
	b.WriteString("|")
	for _, in := range s.Inputs {
		b.WriteString(in.Name + in.ID + in.Type + ",")
	}
	b.WriteString("|")
	for _, c := range s.Controls {
		if len(c.Labels) > 0 {
			b.WriteString(c.Labels[0] + ",")
		}
	}
	fmt.Fprintf(&b, "|%d", len(s.Text))
	return b.String()
}

// cuDoc is a document the rung reads and presses in: the top one (frame "")
// or a sub-frame, with the handle base Locate reads its elements by.
type cuDoc struct {
	frame string
	base  int
}

// cuDocs lists the documents in the order the rung looks for an option: the
// control's own, the top one, then the other sub-frames. None when the frame
// the application lives in is not among them.
func (s *session) cuDocs(ctx context.Context) []cuDoc {
	docs, own := []cuDoc{{}}, s.frame == ""
	for n, f := range subFrames(ctx, s.tab) {
		d := cuDoc{frame: f.ID, base: (n + 1) * frameBase}
		switch {
		case f.ID == s.frame:
			docs, own = append([]cuDoc{d}, docs...), true
		case len(docs) < maxCUDocs:
			docs = append(docs, d)
		}
	}
	if !own {
		return nil
	}
	return docs
}

// cuTarget is what the probe found to press for a row's write.
type cuTarget struct {
	Found    bool     `json:"found"`
	I        int      `json:"i"`
	Tag      string   `json:"tag"`
	Type     string   `json:"type"`
	Multiple bool     `json:"multiple"`
	Checked  bool     `json:"checked"`
	Matched  int      `json:"matched"`
	Editable bool     `json:"editable"`
	Popup    []string `json:"popup"`
	// Expanded is set when the list reports itself open: a press would close it.
	Expanded bool `json:"expanded"`
}

// cuRun is one row's trusted input: the documents it may press in and the
// ones whose engine holds the page's input for it.
type cuRun struct {
	s      *session
	holder inputHolder
	docs   []cuDoc
	held   []string
}

func (c *cuRun) eval(ctx context.Context, d cuDoc, cmd string, arg, out any) error {
	if d.frame == "" {
		return c.s.tab.Eval(ctx, probeExpr(cmd, arg), out)
	}
	return c.s.tab.EvalFrame(ctx, d.frame, probeExpr(cmd, arg), out)
}

// hold brackets the input of a document before the rung's first input there:
// every press, and the keys typed into a native select, which is not pressed.
func (c *cuRun) hold(ctx context.Context, d cuDoc) error {
	if slices.Contains(c.held, d.frame) {
		return nil
	}
	if err := c.holder.HoldInputIn(ctx, d.frame); err != nil {
		return err
	}
	c.held = append(c.held, d.frame)
	return nil
}

// release ends every hold; the control's document learns the value the page
// now holds as the run's own write.
func (c *cuRun) release(ctx context.Context, wrote []engine.Resolution) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, f := range c.held {
		var ws []engine.Resolution
		if f == c.docs[0].frame {
			ws = wrote
		}
		_ = c.holder.ReleaseInputIn(rctx, f, ws)
	}
}

// write writes one row by trusted input and re-reads it. It returns the step
// that failed, or "" once the page holds the value.
func (c *cuRun) write(ctx context.Context, rows []engine.Row, r engine.Row, sel string, w cuWrite, options []string, before string) (step string) {
	if len(c.docs) == 0 {
		return "the frame the application lives in is not reachable"
	}
	var wrote []engine.Resolution
	defer func() { c.release(ctx, wrote) }()
	doc, want := c.docs[0], firstNonEmpty(w.option, w.value)
	text := r.Kind == "text" || r.Kind == "textarea"
	option := want
	if text || r.Kind == "checkbox" && len(options) <= 1 {
		option = ""
	}
	var t cuTarget
	if err := c.eval(ctx, doc, "cuTarget", map[string]any{"sel": sel, "kind": r.Kind, "option": option}, &t); err != nil || !t.Found {
		return "the control is no longer on the page"
	}
	switch r.Kind {
	case "text", "textarea":
		if step := c.typeInto(ctx, doc, t, firstNonEmpty(w.value, w.option)); step != "" {
			return step
		}
	case "select":
		if step := c.typeAhead(ctx, doc, t, want); step != "" {
			return step
		}
	case "custom-listbox":
		if !t.Expanded {
			if step := c.press(ctx, doc, t.I, "the list"); step != "" {
				return step
			}
		}
		if step := c.pickOption(ctx, t, sel, want); step != "" {
			return step
		}
	default:
		switch {
		case t.Matched != 1:
			return "no single choice reads as the answer"
		case t.Checked:
			return "the choice is already marked, and pressing it could clear it"
		}
		if step := c.press(ctx, doc, t.I, "the choice"); step != "" {
			return step
		}
	}
	if !c.s.cuHolds(ctx, rows, r.Selector, want, before, text) {
		return "the page does not hold the value after the write"
	}
	wrote = []engine.Resolution{{Selector: sel, Outcome: "filled", Value: w.value, OptionLabel: w.option}}
	return ""
}

// press presses the element of handle i in d with one trusted click once the
// press gate allows both what the element reads and what stands at the point.
func (c *cuRun) press(ctx context.Context, d cuDoc, i int, what string) string {
	if err := c.hold(ctx, d); err != nil {
		return "the page's input could not be held: " + err.Error()
	}
	loc, err := Locate(ctx, c.s.tab, d.base+i)
	switch {
	case err != nil:
		return what + " could not be found: " + err.Error()
	case !loc.Found || !loc.Visible:
		return what + " is not on screen"
	case loc.Disabled:
		return what + " is disabled"
	case !loc.Hit:
		return what + " is covered by " + firstNonEmpty(strings.TrimSpace(loc.Blocker), "another element")
	}
	reads := [][]string{loc.Labels}
	if len(loc.AtLabels) > 0 {
		reads = append(reads, loc.AtLabels)
	}
	for _, labels := range reads {
		dec := gates.PressControl(gates.ControlInput{Unattended: c.s.e.unattended(), Stage: gates.StageComputerUse, Found: true, Labels: labels, FormSubmitter: loc.Submitter, Link: loc.Link, InFrame: d.base > 0})
		if err := c.s.r.audit(c.s.e, dec, c.s.e.Run.Input.URL, false); err != nil {
			return "the press could not be audited: " + err.Error()
		}
		if !dec.Allowed() {
			return what + " was refused by the press gate (" + dec.Code + ")"
		}
	}
	if err := c.s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return what + " could not be pressed: " + err.Error()
	}
	return ""
}

// typeInto clicks into a text control, selects what it holds, types the value
// and leaves the control with Tab.
func (c *cuRun) typeInto(ctx context.Context, d cuDoc, t cuTarget, value string) string {
	switch {
	case t.Tag != "textarea" && !(t.Tag == "input" && cuTextTypes[t.Type]):
		return "the control is not a text box"
	case !t.Editable:
		return "the box is read-only"
	}
	if step := c.press(ctx, d, t.I, "the box"); step != "" {
		return step
	}
	var focused bool
	if err := c.eval(ctx, d, "focus", t.I, &focused); err != nil || !focused {
		return "the box did not take the focus"
	}
	if err := c.s.tab.TypeText(ctx, value); err != nil {
		return "typing failed: " + err.Error()
	}
	if err := c.s.tab.PressKey(ctx, "Tab"); err != nil {
		return "leaving the box failed: " + err.Error()
	}
	return ""
}

// typeAhead selects a native select's option by typing its label into the
// focused list, one key at a time until the list shows it. A native select is
// never clicked: its menu is the system's, outside the page.
func (c *cuRun) typeAhead(ctx context.Context, d cuDoc, t cuTarget, want string) string {
	switch {
	case t.Tag != "select" || t.Multiple:
		return "the control is not a single-choice list"
	case !t.Editable:
		return "the list is disabled"
	}
	if err := c.hold(ctx, d); err != nil {
		return "the page's input could not be held: " + err.Error()
	}
	var focused bool
	if err := c.eval(ctx, d, "focus", t.I, &focused); err != nil || !focused {
		return "the list did not take the focus"
	}
	for _, ch := range want {
		if err := c.s.tab.TypeText(ctx, string(ch)); err != nil {
			return "typing failed: " + err.Error()
		}
		var cur string
		if c.eval(ctx, d, "cuSelected", t.I, &cur) == nil && normValue(cur) == normValue(want) {
			if err := c.s.tab.PressKey(ctx, "Tab"); err != nil {
				return "leaving the list failed: " + err.Error()
			}
			return ""
		}
	}
	return "typing the option's label did not select it"
}

// pickOption finds the option the answer names after the list was pressed
// open, in the control's document, the top one and the other frames, and
// presses it. The popup is the one the control names now: a list may name it
// only once open.
func (c *cuRun) pickOption(ctx context.Context, t cuTarget, sel, want string) string {
	deadline := time.Now().Add(cuOpenWait)
	several := false
	for {
		popup := t.Popup
		var now cuShown
		if c.eval(ctx, c.docs[0], "cuOptions", map[string]any{"sel": sel}, &now) == nil && len(now.Popup) > 0 {
			popup = now.Popup
		}
		for k, d := range c.docs {
			arg := map[string]any{"name": want, "popup": popup}
			if k == 0 {
				arg["trigger"] = t.I
			}
			var o struct {
				Found    bool
				I, Count int
			}
			if c.eval(ctx, d, "cuOption", arg, &o) != nil {
				continue
			}
			if o.Found {
				return c.press(ctx, d, o.I, "the option")
			}
			several = several || o.Count > 1
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, cuPoll) {
			if several {
				return "several options read as the answer"
			}
			return "no option reads as the answer once the list was pressed"
		}
	}
}

// cuShown is what an open list shows: its option names, the popup its control
// names now, and whether the focus is in the control or that popup.
type cuShown struct {
	Options []string `json:"options"`
	Popup   []string `json:"popup"`
	Focused bool     `json:"focused"`
}

// choose writes an unread list (CO-1): it presses the list open as write does,
// reads the options it shows, has the matcher resolve the entry the abstain
// named against them and picks the one it chose, then re-reads the row. A list
// it picks nothing from is closed with Escape, sent only while the focus is in
// the list. It returns the write, the options read and the step that failed,
// or "" once the page holds it.
func (c *cuRun) choose(ctx context.Context, runID string, rows []engine.Row, r engine.Row, sel string, w cuWrite, legalOn bool, before string) (cuWrite, []string, string) {
	if len(c.docs) == 0 {
		return w, nil, "the frame the application lives in is not reachable"
	}
	var wrote []engine.Resolution
	defer func() { c.release(ctx, wrote) }()
	doc := c.docs[0]
	var t cuTarget
	if err := c.eval(ctx, doc, "cuTarget", map[string]any{"sel": sel, "kind": r.Kind}, &t); err != nil || !t.Found {
		return w, nil, "the control is no longer on the page"
	}
	if r.Kind == "custom-listbox" && !t.Expanded {
		if step := c.press(ctx, doc, t.I, "the list"); step != "" {
			return w, nil, step
		}
	}
	shown := c.shown(ctx, sel)
	// An autocomplete box lists its options on ArrowDown, not on a press (n8n's
	// Ashby state list): the key is pressed once when the press showed none.
	if len(shown.Options) == 0 && r.Kind == "custom-listbox" && t.Editable && c.hold(ctx, doc) == nil && c.s.tab.PressKey(ctx, "ArrowDown") == nil {
		shown = c.shown(ctx, sel)
	}
	pick, why := c.s.cuResolve(ctx, runID, r, w, shown.Options, legalOn)
	if why != "" {
		if shown.Focused {
			err := c.hold(ctx, doc)
			if err == nil {
				err = c.s.tab.PressKey(ctx, "Escape")
			}
			if err != nil {
				why += "; the list could not be closed: " + err.Error()
			}
		}
		return w, shown.Options, why
	}
	want := firstNonEmpty(pick.option, pick.value)
	var step string
	if r.Kind == "select" {
		step = c.typeAhead(ctx, doc, t, want)
	} else {
		step = c.pickOption(ctx, t, sel, want)
	}
	if step == "" && !c.s.cuHolds(ctx, rows, r.Selector, want, before, false) {
		step = "the page does not hold the value after the write"
	}
	if step == "" {
		wrote = []engine.Resolution{{Selector: sel, Outcome: "filled", Value: pick.value, OptionLabel: pick.option}}
	}
	return pick, shown.Options, step
}

// shown reads, for up to cuOpenWait, the options a list shows: in the control's
// own document, then in the others by the popup the control names.
func (c *cuRun) shown(ctx context.Context, sel string) cuShown {
	deadline := time.Now().Add(cuOpenWait)
	for {
		var own cuShown
		if c.eval(ctx, c.docs[0], "cuOptions", map[string]any{"sel": sel}, &own) == nil && len(own.Options) > 0 {
			return own
		}
		for _, d := range c.docs[1:] {
			var o cuShown
			if c.eval(ctx, d, "cuOptions", map[string]any{"popup": own.Popup}, &o) == nil && len(o.Options) > 0 {
				o.Focused = own.Focused
				return o
			}
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, cuPoll) {
			return own
		}
	}
}

// cuHolds re-reads the control, as writeAnswers does, until the page holds
// what the rung wrote or the settle time has passed.
func (s *session) cuHolds(ctx context.Context, rows []engine.Row, sel, want, before string, text bool) bool {
	settle := cuSettle
	if text {
		settle = cuTextSettle
	}
	deadline := time.Now().Add(settle)
	for {
		if got, err := s.inspect(ctx); err == nil {
			held, _ := indexHeld(got, rows)
			if cur, ok := held[sel]; ok && cuHeld(text, want, before, cur.Value) {
				return true
			}
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, cuPoll) {
			return false
		}
	}
}

// cuHeld reports whether the page holds what the rung wrote: the value itself;
// for a text control also the page's own formatting of it (a value new since
// the rung acted, of the same polarity); for a choice also a display that
// shows the label with the same polarity.
func cuHeld(text bool, want, before, now string) bool {
	switch {
	case normValue(now) == normValue(want):
		return true
	case isPlaceholder(now) || normValue(now) == normValue(before):
		return false
	case text:
		return verify(want, now) == writeKept
	}
	return strings.Contains(normValue(now), normValue(want)) && polarity(now) == polarity(want)
}

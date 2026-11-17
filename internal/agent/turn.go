package agent

import (
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/llm"
)

// Channel is where a turn came from; it selects the permission envelope.
type Channel string

const (
	ChannelDashboard Channel = "dashboard"
	ChannelTelegram  Channel = "telegram"
	ChannelScheduled Channel = "scheduled"
	ChannelDigest    Channel = "digest"
)

// allows says whether the channel's envelope contains a permission class.
// Telegram has all four; its two apply tools are refused by name in the guard
// chain. Scheduled and digest turns may read and draft only.
func (c Channel) allows(cl Class) bool {
	switch c {
	case ChannelDashboard, ChannelTelegram:
		return cl.valid()
	case ChannelScheduled, ChannelDigest:
		return cl == ClassRead || cl == ClassDraft
	}
	return false
}

// offers says whether the channel's envelope holds a tool. The computer-use
// tools are refused by name outside the dashboard, because the Telegram
// envelope holds every class and the scheduled one holds reads (AC-2), and so
// are the task tools: a task works in a dashboard conversation.
func (c Channel) offers(tl Tool) bool {
	return c.allows(tl.Class) && (c == ChannelDashboard || !dashboardOnly[tl.Name] && !taskTools[tl.Name])
}

// taskTools are the delegated-task tools.
var taskTools = map[string]bool{ToolDelegateTask: true, ToolTaskUpdate: true, ToolTaskAsk: true, ToolTaskDone: true}

// dashboardOnly are the computer-use tools: the desktop tools, the two that
// answer a stopped run's questions, the apply tools that work its kept tab and
// load_skill, which names them. Only a dashboard turn from this computer gets
// them.
var dashboardOnly = map[string]bool{
	ToolDesktopWindows: true, ToolDesktopLook: true, ToolDesktopClick: true, ToolDesktopType: true, ToolDesktopKeys: true, ToolDesktopScroll: true,
	ToolRunQuestions: true, ToolAnswerRunQuestion: true, ToolLoadSkill: true,
	ToolApplyPage: true, ToolApplyFill: true, ToolApplyAnswer: true, ToolApplyPress: true, ToolApplyReview: true, ToolApplyLeave: true, ToolApplyContinue: true,
}

// permits says whether the turn may use a tool: its channel's envelope holds
// the tool and the turn does not withhold it. A remote turn reads and drafts,
// and none of the computer-use tools. A "Let the agent finish" turn reads,
// drafts and uses the computer-use tools only: its origin is the button, which
// counts as the user's own request, yet the model chooses its calls, so no
// other tool may borrow the button (an apply tool would read it as an explicit
// request to submit).
func (t *Turn) permits(tl Tool) bool {
	readOrDraft := tl.Class == ClassRead || tl.Class == ClassDraft
	switch {
	case t.Remote && (!readOrDraft || dashboardOnly[tl.Name]):
		return false
	case t.FinishRun != "" && !readOrDraft && !dashboardOnly[tl.Name]:
		return false
	}
	return t.Channel.offers(tl) && !slices.Contains(t.Withheld, tl.Name)
}

// caller is the audit table's caller for a channel.
func (c Channel) caller() gates.Caller {
	switch c {
	case ChannelDashboard:
		return gates.CallerAskAgent
	case ChannelTelegram:
		return gates.CallerTelegram
	}
	return gates.CallerScheduler
}

// Origin says who chose a call: the model loop, the intent router, or a button
// the user pressed. The last two count as named by the user, because their
// arguments come from the user's own message or act.
type Origin string

const (
	OriginModel  Origin = "model"
	OriginRouter Origin = "router"
	OriginButton Origin = "button"
)

// Turn is what the guard chain knows about one turn. Its source trust starts
// from the taint carried by the conversation and turns untrusted for good when
// a tool that returns outside content answers.
type Turn struct {
	ID          string
	SessionID   string
	Channel     Channel
	Origin      Origin
	UserMessage string
	// ButtonLabel is the label of the button that started a call whose origin
	// is OriginButton; a confirmation card shows it as the purpose.
	ButtonLabel string
	// FinishRun is the stopped application run a "Let the agent finish" turn
	// finishes; the run tools act on no other run in it.
	FinishRun string
	// Task is the open delegated task of the conversation, "" for none.
	// Mandate is that task's mandate in a turn the user typed: the user's own
	// message that delegated it, which counts as one of the user's messages of
	// the turn for intent (owner decision DT-2), never as an explicit request
	// to submit. TaskRuns are the runs the conversation started, which the
	// task's turns may work.
	Task     string
	Mandate  string
	TaskRuns []string
	// Continues marks a turn the task started by itself: its UserMessage is
	// the mandate, and it never asks to submit, so the runs it starts stop at
	// Submit for the user.
	Continues bool
	// CarriedTaint is set when the packed context holds a marked message or a
	// marked checkpoint. It binds model-chosen calls only: a call chosen by the
	// router or a button involves no model context.
	CarriedTaint bool
	// Remote is set for a message from a remote device. Such a turn may read
	// and draft; tools that write locally or act externally are not offered and
	// a call to one is denied.
	Remote bool
	// Withheld names tools the turn is not offered although its channel's
	// envelope contains their class; a call to one is denied.
	Withheld []string
	// Confirmed is set when the call being run was approved on its card.
	Confirmed bool
	// TakesImages says the turn's model takes images, so a tool result's
	// images reach it; otherwise it gets the text alone (AC-3).
	TakesImages bool
	// Tier is the model tier the intent classifier chose, set before the
	// process starts: light, heavy, or empty when the classifier did not run.
	Tier string

	mu      sync.Mutex
	outside bool
	// steers are the steering messages delivered to the model in this turn,
	// which are the user's own words like the message; removals counts the
	// skip-list removals the confirmation step let through.
	steers   []string
	removals int
	audit    gates.TurnAudit
	// paths are the local paths the turn's tool results reported, which the
	// disclosure gate lets an answer repeat; env collects what the tools
	// handed to the answer's envelope.
	paths map[string]bool
	env   Envelope
	// looks are the turn's latest look at each window, which a desktop action
	// binds to.
	looks lookCache
	// skill is the skill the turn has loaded, "" for none.
	skill string
	// taskMark is the task's last progress when the turn began, against which
	// its end tells whether the turn moved the task.
	taskMark time.Time
}

// LoadSkill records the skill the turn runs under.
func (t *Turn) LoadSkill(name string) {
	t.mu.Lock()
	t.skill = name
	t.mu.Unlock()
}

// Skill is the name of the skill the turn has loaded.
func (t *Turn) Skill() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.skill
}

// background says whether nobody waits at the turn as it runs: a schedule's or
// a digest's turn, or one a delegated task took by itself. Background turns
// never hold the last process slot, which stays free for a person.
func (t *Turn) background() bool {
	return t.Continues || t.Channel == ChannelScheduled || t.Channel == ChannelDigest
}

// modelTask is the routing row that serves the turn's model calls: the light
// row for a light turn, the agent kernel otherwise.
func (t *Turn) modelTask() string {
	if t.Tier == TierLight {
		return llm.AgentLightTask
	}
	return llm.AgentTask
}

// ReportPaths records the local paths a tool result of this turn mentioned.
func (t *Turn) ReportPaths(text string) {
	found := PathsIn(text)
	if len(found) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paths == nil {
		t.paths = map[string]bool{}
	}
	for _, p := range found {
		t.paths[p] = true
	}
}

func (t *Turn) reportedPaths() map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]bool, len(t.paths))
	for p := range t.paths {
		out[p] = true
	}
	return out
}

// Collect adds what a tool offered to the final answer's envelope: the sources
// of its claims, what it could not establish and a next step for the user.
func (t *Turn) Collect(r ToolResult) {
	if len(r.Citations) == 0 && len(r.Limitations) == 0 && r.NextAction == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.env.Citations = append(t.env.Citations, r.Citations...)
	t.env.Limitations = append(t.env.Limitations, r.Limitations...)
	if r.NextAction != "" {
		t.env.NextAction = r.NextAction
	}
}

// EnvelopeFor wraps the answer text with what the turn's tools collected.
func (t *Turn) EnvelopeFor(text string) Envelope {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.env
	e.Text = text
	e.Citations = dedupCitations(e.Citations)
	e.Limitations = dedupStrings(e.Limitations)
	return e
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dedupCitations(in []Citation) []Citation {
	seen := map[string]bool{}
	var out []Citation
	for _, c := range in {
		// A page is one source whatever the call that found it; a source
		// without a page is told apart by its label.
		key := c.Label
		if c.URL != "" {
			key = c.URL
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// explicitApply reports whether the user asked for an application: a call a
// button or the router made, or a message that names the verb or asks to
// correct a submitted application in the user's own words. A call the
// model chose from its own reasoning is never explicit, and neither is a
// task's mandate: a turn the task started never asks to submit (DT-2).
func (t *Turn) explicitApply() bool {
	if t.Continues {
		return false
	}
	texts := t.userTexts()
	return t.Origin != OriginModel || namesOf(texts, applyVerbs) || gates.CorrectionAsked(ownTextOf(texts))
}

// Steer records a steering message delivered to the model.
func (t *Turn) Steer(text string) {
	t.mu.Lock()
	t.steers = append(t.steers, text)
	t.mu.Unlock()
}

// userTexts are the user's messages of the turn: the message, then each
// steering message, every one read with the same question and pasted-text
// rules.
func (t *Turn) userTexts() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{t.UserMessage}, t.steers...)
}

// intentTexts are the user's messages of the turn and, in a conversation with
// an open task, the task's mandate: what the user asked for, which the guard
// reads for intent.
func (t *Turn) intentTexts() []string {
	texts := t.userTexts()
	if t.Mandate != "" {
		texts = append(texts, t.Mandate)
	}
	return texts
}

// names reports whether one of the user's messages of the turn, or the
// mandate of its task, names a verb.
func (t *Turn) names(vs verbSet) bool { return namesOf(t.intentTexts(), vs) }

func namesOf(texts []string, vs verbSet) bool {
	for _, m := range texts {
		if namesVerb(m, vs) {
			return true
		}
	}
	return false
}

// ownText is the user's own words of every message of the turn and of its
// task's mandate.
func (t *Turn) ownText() string { return ownTextOf(t.intentTexts()) }

func ownTextOf(texts []string) string {
	var parts []string
	for _, m := range texts {
		parts = append(parts, OwnText(m))
	}
	return strings.Join(parts, "\n")
}

// typedText is the user's own words of the messages they typed in the turn.
// The message a button sends is the system's, written from records that can
// carry a posting's text, so a turn a button started has its steering
// messages only.
func (t *Turn) typedText() string {
	texts := t.userTexts()
	if t.Origin == OriginButton {
		texts = texts[1:]
	}
	var parts []string
	for _, m := range texts {
		parts = append(parts, OwnText(m))
	}
	return strings.Join(parts, "\n")
}

// forbidsSubmit reports whether any message of the turn, or its task's
// mandate, forbids a submit.
func (t *Turn) forbidsSubmit() bool {
	for _, m := range t.intentTexts() {
		if forbidsSubmit(m) {
			return true
		}
	}
	return false
}

// countRemoval returns how many skip-list removals were let through before
// this one and counts this one.
func (t *Turn) countRemoval() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removals++
	return t.removals - 1
}

// Untrusted reports the turn's source trust as the gates read it.
func (t *Turn) Untrusted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outside || (t.CarriedTaint && t.Origin == OriginModel)
}

// CarryTaint records that the context packed for the turn holds a marked
// message or checkpoint.
func (t *Turn) CarryTaint() {
	t.mu.Lock()
	t.CarriedTaint = true
	t.mu.Unlock()
}

// MarkOutside records that outside content was read in this turn.
func (t *Turn) MarkOutside() {
	t.mu.Lock()
	t.outside = true
	t.mu.Unlock()
}

// admit reports whether an audit row is the first of its kind this turn.
func (t *Turn) admit(r gates.AuditRow) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.audit.Admit(r)
}

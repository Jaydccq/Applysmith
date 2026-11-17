package llm

import "time"

// Class is the model tier a preset assigns per task.
type Class string

const (
	ClassFast      Class = "fast"
	ClassStandard  Class = "standard"
	ClassDeep      Class = "deep"
	ClassEmbedding Class = "embedding"
)

// Thinking levels, lowest to highest.
const (
	ThinkOff     = "off"
	ThinkMinimal = "minimal"
	ThinkLow     = "low"
	ThinkMedium  = "medium"
	ThinkHigh    = "high"
	ThinkXHigh   = "xhigh"
)

var thinkingLevels = []string{ThinkOff, ThinkMinimal, ThinkLow, ThinkMedium, ThinkHigh, ThinkXHigh}

func validThinking(l string) bool {
	for _, v := range thinkingLevels {
		if v == l {
			return true
		}
	}
	return false
}

// Task is one registry entry: the preset defaults of a task id.
type Task struct {
	ID        string
	Class     Class
	Thinking  string
	MaxTokens int
	Timeout   time.Duration
	// Group is the display group in the dashboard; every task is in exactly one.
	Group string
	// Needs is the capability the task requires, empty for plain completion.
	Needs Capability
	// Purpose says in words what the task is used for (the Models table shows it).
	Purpose string
}

// Display groups.
const (
	GroupEvaluation = "evaluation"
	GroupApply      = "apply"
	GroupDocuments  = "documents"
	GroupMail       = "mail"
	GroupKnowledge  = "knowledge"
	GroupAgent      = "agent"
)

// Groups lists the display groups in order.
var Groups = []string{GroupEvaluation, GroupApply, GroupDocuments, GroupMail, GroupKnowledge, GroupAgent}

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

var tasks = []Task{
	{"deep-eval", ClassDeep, ThinkMedium, 24000, sec(600), GroupEvaluation, "", "Scores a posting against your profile and writes its report"},
	{"quick-screen", ClassFast, ThinkLow, 4000, sec(60), GroupEvaluation, "", "Screens a posting before the full evaluation"},
	{"option-pick", ClassFast, ThinkLow, 2048, sec(20), GroupApply, "", "Picks the form option that matches your answer"},
	{"narrative", ClassStandard, ThinkLow, 2048, sec(45), GroupDocuments, "", "Writes free-text form answers from your profile"},
	{"company-explainer", ClassStandard, ThinkLow, 4096, sec(90), GroupKnowledge, "", "Explains what a company does for an answer"},
	{"research-search", ClassStandard, ThinkLow, 8000, sec(120), GroupKnowledge, "", "Looks up company facts on the web"},
	{"story-extraction", ClassDeep, ThinkLow, 24000, sec(300), GroupKnowledge, "", "Finds stories for the knowledge base in your documents"},
	{"gmail-summary", ClassFast, ThinkOff, 1024, sec(45), GroupMail, "", "Summarises a job email"},
	{"gmail-digest", ClassStandard, ThinkOff, 4096, sec(90), GroupMail, "", "Writes the morning and evening email brief"},
	{"feed-classify", ClassFast, ThinkOff, 1024, sec(30), GroupEvaluation, "", "Decides whether a feed post is hiring"},
	{"jd-extract", ClassFast, ThinkOff, 512, sec(20), GroupEvaluation, "", "Reads the title, company and location of a pasted posting"},
	{"jd-decode", ClassStandard, ThinkLow, 24000, sec(180), GroupEvaluation, "", "Explains a posting in plain words"},
	{"compaction", ClassStandard, ThinkOff, 4096, sec(60), GroupAgent, "", "Shortens a long Ask Agent conversation"},
	{"agent-kernel", ClassDeep, ThinkMedium, 16000, sec(180), GroupAgent, CapTools, "Runs Ask Agent"},
	{AgentLightTask, ClassFast, ThinkLow, 8000, sec(180), GroupAgent, CapTools, "Runs Ask Agent turns that need a lookup or a short answer; routing it turns intent routing on"},
	{IntentTask, ClassFast, ThinkOff, 64, sec(4), GroupAgent, "", "Reads each new Ask Agent message and picks the light or heavy model for the turn"},
	{"embedding", ClassEmbedding, "", 0, sec(30), GroupKnowledge, CapEmbeddings, "Indexes the knowledge base for search"},
	{"memory-extract", ClassStandard, ThinkOff, 8000, sec(300), GroupAgent, "", "Saves what Ask Agent should remember"},
	{"agent-improve", ClassDeep, ThinkMedium, 16000, sec(300), GroupAgent, "", "Proposes changes to Ask Agent's guidance from conversations that went wrong"},
	{"agent-judge", ClassStandard, ThinkLow, 2048, sec(120), GroupAgent, "", "Compares Ask Agent answers blind for self-improvement"},
	{"apply-check", ClassFast, ThinkOff, 512, sec(20), GroupApply, "", "Reviews a filled application page before Submit"},
	{"apply-complete", ClassStandard, ThinkOff, 2048, sec(45), GroupApply, "", "Answers the questions an application leaves open"},
	{"apply-page-decision", ClassFast, ThinkOff, 256, sec(10), GroupApply, "", "Decides what to do on an application page"},
	{"apply-fix", ClassStandard, ThinkOff, 512, sec(20), GroupApply, "", "Corrects an answer an application page refused"},
	{"suggestion-review", ClassStandard, ThinkLow, 2048, sec(60), GroupApply, "", "Sorts answer-bank suggestions into the ones worth an entry"},
	{"signal-assess", ClassStandard, ThinkLow, 4096, sec(60), GroupMail, "", "Assesses a hiring signal"},
	{"resume-write", ClassDeep, ThinkOff, 16000, sec(300), GroupDocuments, "", "Writes a résumé for a posting"},
	{"document-review", ClassStandard, ThinkLow, 6144, sec(60), GroupDocuments, "", "Checks a written document against the posting"},
	{"cover-letter", ClassStandard, ThinkOff, 6144, sec(120), GroupDocuments, "", "Writes a cover letter"},
	{"message-draft", ClassStandard, ThinkLow, 2048, sec(60), GroupDocuments, "", "Drafts an outreach or follow-up message"},
	{"interview-prep", ClassDeep, ThinkLow, 16000, sec(300), GroupDocuments, "", "Writes an interview preparation pack"},
}

// Tasks returns the registry in documentation order.
func Tasks() []Task { return append([]Task(nil), tasks...) }

// LookupTask returns the registry entry of a task id.
func LookupTask(id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// optIn lists the tasks no preset routes: the owner turns the feature on by
// routing one, and until then it is not a missing route.
var optIn = map[string]bool{AgentLightTask: true}

// exemptFromDowngrade lists the tasks the spend limits never move to a cheaper
// model: checks on a live application, and grounded prose that a smaller
// model measurably degrades.
var exemptFromDowngrade = map[string]bool{
	"option-pick": true, "apply-check": true, "apply-complete": true,
	"apply-page-decision": true, "apply-fix": true, "gmail-digest": true,
}

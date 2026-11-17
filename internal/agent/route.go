package agent

import (
	"encoding/json"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/tracker"
)

// Route is the intent router's answer: one tool and its arguments, and the id
// of the rule that chose it.
type Route struct {
	RuleID string
	Tool   string
	Args   json.RawMessage
}

// Fallback reasons: why the model loop gets the message.
const (
	FallbackEmpty     = "empty"
	FallbackMultiStep = "multi_step"
	FallbackNoRule    = "no_rule"
	// FallbackAmbiguous is a message about two of the places the router reads
	// (the tracker and the mail, say): no rule is allowed to win by coming first.
	FallbackAmbiguous = "ambiguous"
)

// Rule ids, in the order the router tries them.
const (
	RuleURLOnly        = "url-only"
	RuleRecentSubmits  = "recent-submissions"
	RuleActivityToday  = "activity-today"
	RuleApplyListRun   = "apply-list-run"
	RuleApplyURL       = "apply-url"
	RuleApplyListState = "apply-list-status"
	RuleTrackerCounts  = "tracker-counts"
	RuleScanSource     = "scan-source"
)

// message is a user message prepared for matching: its URLs lifted out, the
// rest folded to lower-case words, and whether it is a question. A question
// mark inside a URL is part of the URL, so it never makes a request a question.
// request marks a message that, after its filler ("please", "can you"),
// starts with a verb of the intent tables: "can you scan linkedin?" and
// "evaluate <url> - what do you make of it?" are requests, "should I apply to
// <url>?" is a question.
type message struct {
	norm     string
	words    []string
	urls     []string
	question bool
	request  bool
}

// asks reports whether the message asks something rather than requests it,
// from the router's own question predicate: the guard's check that the user
// named an action (namesVerb) reads it, so a question never counts as a
// request there either. The router's action rules are stricter and take no
// question-shaped message at all.
func (m message) asks() bool { return m.question && !m.request }

var (
	httpURL    = lazyregexp.New(`(?i)\bhttps?://[^<>"` + urlStop + `]+`)
	fillerHead = lazyregexp.New(`^((hey|hi|hello|ok|okay|so|just|now|please|can you|could you|would you|will you|go ahead and|i want you to|i would like you to|i d like you to|i want to|i need you to) )+`)
	fillerTail = lazyregexp.New(`( (please|thanks|thank you|for me|now|again))+$`)
)

// hanQuestion are the Chinese words that make a message a question: the
// question particles, "whether", "should or not", "is it", "worth it or not",
// "should", "how", "how to", "why" and "what".
var hanQuestion = []string{"\u5417", "\u5462", "\u662f\u5426", "\u8981\u4e0d\u8981", "\u8be5\u4e0d\u8be5", "\u662f\u4e0d\u662f", "\u503c\u4e0d\u503c\u5f97",
	"\u5e94\u4e0d\u5e94\u8be5", "\u5e94\u8be5", "\u600e\u4e48", "\u5982\u4f55", "\u4e3a\u4ec0\u4e48", "\u4ec0\u4e48"}

// asksHere reports whether a Chinese question word asks: one whose clause ends
// at a colon is a form question the message answers ("whether you are in
// process elsewhere: No"). Such answers to a stopped run had made "fill these
// in my words" a question, which names no verb.
func asksHere(_ []rune, after string) bool {
	i := strings.IndexAny(after, "\uff0c\u3002\uff1b\uff01,.;!\n:\uff1a")
	return i < 0 || after[i] != ':' && !strings.HasPrefix(after[i:], "\uff1a")
}

func prepare(text string) message {
	var m message
	// A quoted line is pasted (a Telegram reply quotes the message it answers),
	// never the request: the guard reads it the same way (ownWords).
	text = quotedLine.ReplaceAllString(text, " ")
	bare := httpURL.ReplaceAllStringFunc(text, func(u string) string {
		m.urls = append(m.urls, strings.TrimRight(u, ".,;:!?)]}'\""))
		return " url "
	})
	m.question = strings.ContainsAny(bare, "?\uff1f") || hanUse(bare, hanQuestion, asksHere)
	words := wordsOf(strings.NewReplacer("'", "", "’", "").Replace(bare))
	m.norm = strings.Join(words, " ")
	m.norm = fillerHead.ReplaceAllString(m.norm, "")
	m.norm = fillerTail.ReplaceAllString(m.norm, "")
	m.words = strings.Fields(m.norm)
	if len(m.words) > 0 {
		w := m.words[0]
		m.request = writeVerbs.words[w] || actionVerbs.words[w] || applyVerbs.words[w]
	}
	if len(m.words) > 0 && !m.question {
		switch m.words[0] {
		case "what", "whats", "which", "how", "hows", "who", "when", "where", "why", "is", "are", "do", "does", "did", "have", "has", "was", "were", "should", "can", "could", "would", "will":
			m.question = true
		}
	}
	return m
}

// multiStepVerbs are the verbs and question words that, after "and", make a
// message more than one request ("how many are evaluated and what is the top
// one"). In Chinese a joining word (then, and also, besides, at the same time)
// followed by a question word does the same.
var (
	multiStepVerbs = wordSet("draft", "write", "compare", "send", "email", "apply", "scan", "evaluate", "summarize", "summarise", "tell", "find", "update", "prepare", "research", "add", "save", "remember", "tailor",
		"what", "whats", "which", "who", "where", "when", "why", "how", "top", "best", "highest", "show", "list")
	hanJoin     = []string{"\u7136\u540e", "\u5e76\u4e14", "\u4ee5\u53ca", "\u53e6\u5916", "\u540c\u65f6"}
	hanMultiAsk = []string{"\u4ec0\u4e48", "\u54ea", "\u8c01", "\u4e3a\u4ec0\u4e48", "\u600e\u4e48", "\u6700\u9ad8", "\u6700\u597d", "\u5217\u51fa", "\u663e\u793a"} // what, which, who, why, how, highest, best, list, show
)

func (m message) multiStep() bool {
	for i, w := range m.words {
		switch w {
		case "then", "afterwards", "also":
			return true
		case "and":
			for _, next := range m.words[i+1:] {
				if multiStepVerbs[next] {
					return true
				}
			}
		}
		for _, j := range hanJoin {
			if k := strings.Index(w, j); k >= 0 && hanUse(strings.Join(m.words[i:], " ")[k+len(j):], hanMultiAsk, func([]rune, string) bool { return true }) {
				return true
			}
		}
	}
	return len(m.urls) > 1
}

// subjectWords are the whole words that say which store a question is about.
// A word inside another word ("ziprecruiter") says nothing.
var subjectWords = map[string]map[string]bool{
	"tracker":   wordSet("tracker", "application", "applications", "applied"),
	"list":      wordSet("list", "queue"),
	"mail":      wordSet("mail", "mails", "email", "emails", "inbox", "gmail"),
	"pipeline":  wordSet("pipeline", "leads"),
	"reports":   wordSet("report", "reports"),
	"schedules": wordSet("schedule", "schedules", "scheduled"),
}

// ambiguous reports whether the message names words of two stores.
func (m message) ambiguous() bool {
	n := 0
	for _, set := range subjectWords {
		for _, w := range m.words {
			if set[w] {
				n++
				break
			}
		}
	}
	return n > 1
}

type routeRule struct {
	id    string
	match func(m message) (tool string, args map[string]any, ok bool)
}

var (
	reRecentSubmits = []*lazyregexp.Regexp{
		lazyregexp.New(`^(what|which)( applications| jobs| postings| roles| ones)? (did i|have i|was|were|has|have|did we|did you|have you|did the agent|did the assistant)( just| already)? (submit|submitted|apply|applied|send|sent|go|gone|went|get|got)\b`),
		lazyregexp.New(`^(what|which) (has|have) (been )?(submitted|sent)\b`),
		lazyregexp.New(`^(show|list|what are|whats|see)( me)?( my| the)?( recent| latest| todays| yesterdays)? submissions( today| so far| lately)?$`),
		lazyregexp.New(`^(recent|latest|todays) submissions$`),
	}
	// "What did you do today", "what happened today", "recap my day", and the
	// Chinese "what did you do today" (optional "you", "all", "some", "things").
	reActivityToday = []*lazyregexp.Regexp{
		lazyregexp.New(`^(what|whats|what all) (did|have|has) (you|we|the agent|the assistant|the app) (do|done|get done|work on|worked on|accomplish|accomplished)( for me)? (today|so far today|today so far)$`),
		lazyregexp.New(`^(what|whats) (happened|has happened|got done|was done|has been done)( for me)? today( so far)?$`),
		lazyregexp.New(`^(summarize|summarise|recap|sum up)( my| the)? (day|today)$`),
		lazyregexp.New("^\u4f60?\u4eca\u5929\u4f60?\u90fd?(\u505a|\u5e72)\u4e86\u4e9b?(\u4ec0\u4e48|\u5565)(\u4e8b\u60c5|\u4e8b)?$"),
	}
	reEvaluateURL  = lazyregexp.New(`^(evaluate|score|assess) (this |the |that )?(job |posting |role |position )?(url |link )?url$`)
	reApplyListRun = lazyregexp.New(`^(apply|go through|work through|run|start) (to |for |from |through )?(all |everything on |every posting on |every job on )?(of )?(the |my )?(todays |days |daily )?(apply )?(list|queue)( for today| today)?$`)
	reApplyURL     = lazyregexp.New(`^(apply|autofill|fill|fill out|fill in) (to |for |on |at )?(this |the |that )?(job |posting |role |position |application )?(url |link )?(at |on |for )?url$`)
	// reNoSubmitTail is a restriction after an apply request ("... but don't
	// submit it"). A routed autofill_application never submits, so the request
	// without the tail asks for the same run. Live, a fill asked this way went
	// to the model for the one call the router makes.
	reNoSubmitTail   = lazyregexp.New(`( but| and)? (do not|dont|without|never) (submit|submitting|send|sending)( it| the application| yet)*$`)
	reApplyListState = []*lazyregexp.Regexp{
		lazyregexp.New(`^(whats|what is|show|show me|give me|check|how is|hows|status of|see|list)( on| in| of)? (the |my |todays |days |daily )*(apply )?(list|queue)( status)?( today| for today| so far)?$`),
		lazyregexp.New(`^(the |my |todays |days )*(apply )?(list|queue) status( today)?$`),
		lazyregexp.New(`^how many (are |postings are |jobs are )?(left |waiting |ready )?(on|in) (the |my |todays |days )*(apply )?(list|queue)( today)?$`),
		lazyregexp.New(`^what is left (to apply to|on the list)( today)?$`),
	}
	reTrackerNoun = lazyregexp.New(`\b(tracker|applications?|applied|pipeline rows|rows|entries)\b`)
	reScan        = lazyregexp.New(`^(run |start |do )?(a |an )?(scan|search)( of| on| for| the| my| new| fresh)* (.+)$`)
	reScanFiller  = lazyregexp.New(`\b(the|my|for|new|fresh|on|a|board|boards|source|portal|site|listings)\b`)
	reScanNoun    = lazyregexp.New(`\b(jobs?|postings?|roles)\b`)

	// The Chinese forms of "evaluate this posting <url>" and "scan <source>":
	// an optional "please" and "help me", the verb (evaluate, assess, analyse,
	// score; scan, search), an optional "a bit" / "once", and for an
	// evaluation an optional "this" and "posting" / "job" / "link"; for a scan
	// an optional "postings on it" after the source.
	reEvaluateURLZh = lazyregexp.New("^(url )?(\u8bf7|\u9ebb\u70e6\u4f60?)?(\u5e2e\u6211|\u7ed9\u6211|\u5e2e\u5fd9)?(\u8bc4\u4f30|\u8bc4\u4ef7|\u5206\u6790|\u6253\u5206)(\u4e00\u4e0b|\u4e0b)?(\u8fd9\u4e2a|\u8fd9\u4efd)?(\u804c\u4f4d|\u5c97\u4f4d|\u5de5\u4f5c|\u94fe\u63a5)?( url)?$")
	reScanZh        = lazyregexp.New("^(?:\u8bf7|\u9ebb\u70e6\u4f60?)?(?:\u5e2e\u6211|\u7ed9\u6211)?(?:\u626b\u63cf|\u641c\u7d22|\u626b|\u641c)(?:\u4e00\u4e0b|\u4e00\u904d|\u4e0b)? ([a-z0-9 -]+?)(?: (?:\u4e0a\u7684|\u7684)?\u65b0?(?:\u804c\u4f4d|\u5c97\u4f4d|\u5de5\u4f5c))?$")
)

func anyMatch(res []*lazyregexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

var routeRules = []routeRule{
	{RuleURLOnly, func(m message) (string, map[string]any, bool) {
		if len(m.urls) == 1 && (m.norm == "url" || !m.question && (reEvaluateURL.MatchString(m.norm) || reEvaluateURLZh.MatchString(m.norm))) {
			return "evaluate_job", map[string]any{"url": m.urls[0]}, true
		}
		return "", nil, false
	}},
	{RuleRecentSubmits, func(m message) (string, map[string]any, bool) {
		if anyMatch(reRecentSubmits, m.norm) {
			return "submissions_recent", map[string]any{}, true
		}
		return "", nil, false
	}},
	{RuleActivityToday, func(m message) (string, map[string]any, bool) {
		if len(m.urls) == 0 && anyMatch(reActivityToday, m.norm) {
			return "activity_today", map[string]any{}, true
		}
		return "", nil, false
	}},
	{RuleApplyListRun, func(m message) (string, map[string]any, bool) {
		if !m.question && len(m.urls) == 0 && reApplyListRun.MatchString(m.norm) {
			return "apply_from_list", map[string]any{}, true
		}
		return "", nil, false
	}},
	{RuleApplyURL, func(m message) (string, map[string]any, bool) {
		if !m.question && len(m.urls) == 1 && reApplyURL.MatchString(reNoSubmitTail.ReplaceAllString(m.norm, "")) {
			return "autofill_application", map[string]any{"url": m.urls[0]}, true
		}
		return "", nil, false
	}},
	{RuleApplyListState, func(m message) (string, map[string]any, bool) {
		if len(m.urls) == 0 && anyMatch(reApplyListState, m.norm) {
			return "apply_list_status", map[string]any{}, true
		}
		return "", nil, false
	}},
	{RuleTrackerCounts, func(m message) (string, map[string]any, bool) {
		if !m.question || len(m.urls) > 0 || len(m.words) > 14 || !reTrackerNoun.MatchString(m.norm) && statusIn(m.words) == "" {
			return "", nil, false
		}
		if len(m.words) < 2 || (m.words[0] != "how" && m.words[0] != "what" && m.words[0] != "whats" && m.words[0] != "show" && m.words[0] != "count") {
			return "", nil, false
		}
		if !onlyCountWords(m.words) {
			return "", nil, false
		}
		args := map[string]any{}
		if st := statusIn(m.words); st != "" {
			args["status"] = st
		}
		return "tracker_query", args, true
	}},
	{RuleScanSource, func(m message) (string, map[string]any, bool) {
		if m.question || len(m.urls) > 0 {
			return "", nil, false
		}
		var named string
		if sub := reScan.FindStringSubmatch(m.norm); sub != nil {
			named = sub[5]
		} else if sub := reScanZh.FindStringSubmatch(m.norm); sub != nil {
			named = sub[1]
		} else {
			return "", nil, false
		}
		// "google jobs" is a source name, "the indeed jobs" is a source and a noun.
		kept := strings.Join(strings.Fields(reScanFiller.ReplaceAllString(named, " ")), "-")
		bare := strings.Join(strings.Fields(reScanNoun.ReplaceAllString(reScanFiller.ReplaceAllString(named, " "), " ")), "-")
		for _, s := range discovery.Sources {
			if s.Name == kept || s.Name == bare {
				return "scan_portal", map[string]any{"source": s.Name}, true
			}
		}
		return "", nil, false
	}},
}

// trackerCountWords make up a question that asks only for the tracker's
// counts or rows: the router answers it with tracker_query's own lines. Any
// other word (highest, should, a company's name) asks for more than that, and
// the model answers it: live, "What's my highest scored application?" got the
// rows in date order instead of the top score.
var trackerCountWords = wordSet(strings.Fields(`how many much count number total what whats which show me list see my the all of are is in at on to
	do i ive have has had there been currently now so far right by each per status stage statuses stages and or
	tracker application applications rows entries jobs roles postings applied`)...)

// onlyCountWords reports whether every word is a counting word or a tracker
// status.
func onlyCountWords(words []string) bool {
	for _, w := range words {
		if !trackerCountWords[w] && statusIn([]string{w}) == "" {
			return false
		}
	}
	return true
}

// statusIn returns the one tracker status a question names, or "" when it
// names none or several.
func statusIn(words []string) string {
	found := ""
	for _, w := range words {
		for _, st := range tracker.Statuses() {
			if strings.EqualFold(w, string(st)) || w == strings.ToLower(string(st))+"s" {
				if found != "" && found != strings.ToLower(string(st)) {
					return ""
				}
				found = strings.ToLower(string(st))
			}
		}
		if w == "applied" && found == "" {
			found = "applied"
		}
	}
	return found
}

// RouteMessage tries the rules in order and returns the first match. When none
// matches it returns a zero Route and the reason the model loop gets the
// message. It looks at the message alone: no memory, no earlier turn, no model.
func RouteMessage(text string) (Route, string) {
	m := prepare(text)
	if m.norm == "" {
		return Route{}, FallbackEmpty
	}
	if m.multiStep() {
		return Route{}, FallbackMultiStep
	}
	if m.ambiguous() {
		return Route{}, FallbackAmbiguous
	}
	for _, r := range routeRules {
		if tool, args, ok := r.match(m); ok {
			b, _ := json.Marshal(args)
			return Route{RuleID: r.id, Tool: tool, Args: b}, ""
		}
	}
	return Route{}, FallbackNoRule
}

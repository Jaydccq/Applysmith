// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"encoding/json"
	"strconv"
)

// Gate classes of a must-have.
const (
	GateNone       = "none"
	GateWorkAuth   = "work_authorization"
	GateClearance  = "security_clearance"
	GateEducation  = "education"
	GateLicense    = "license"
	GateProgram    = "program_eligibility"
	GateLanguage   = "required_language"
	GateExperience = "experience_threshold"
	GateOnsite     = "onsite_location"
)

// Item is one requirement or signal with the model's evidence for it.
type Item struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Source    string  `json:"source,omitempty"`
	Hit       float64 `json:"hit_level"`
	Evidence  string  `json:"evidence"`
	GateClass string  `json:"gate_class,omitempty"`
}

// Flag is one red flag.
type Flag struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Level    float64 `json:"level"`
	Evidence string  `json:"evidence"`
}

// PostingFacts is what the model read from the posting.
type PostingFacts struct {
	Title        string `json:"title"`
	Company      string `json:"company"`
	Location     string `json:"location"`
	WorkModel    string `json:"work_model"`
	Compensation struct {
		Text     string   `json:"text"`
		Minimum  *float64 `json:"minimum"`
		Maximum  *float64 `json:"maximum"`
		Currency string   `json:"currency"`
		Period   string   `json:"period"`
	} `json:"compensation"`
	EmploymentType string `json:"employment_type"`
	CompanySize    string `json:"company_size"`
	RoleFamily     string `json:"role_family"`
}

// Sections is the model's prose.
type Sections struct {
	Snapshot        string    `json:"snapshot"`
	LevelTrajectory string    `json:"level_trajectory"`
	PayNotes        string    `json:"pay_notes"`
	TailoringPlan   []string  `json:"tailoring_plan"`
	InterviewPrep   []string  `json:"interview_prep"`
	Keywords        []string  `json:"keywords"`
	Gaps            []GapNote `json:"gaps,omitempty"`
}

// Gap kinds: what a missed must-have is to the seeker.
const (
	GapEligibility = "eligibility"
	GapCore        = "core"
	GapPreference  = "preference"
)

// GapNote is the model's triage of one must-have the profile does not fully meet.
type GapNote struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Nearest    string `json:"nearest"`
	Mitigation string `json:"mitigation"`
}

// DeepOutput is the structured reply of the deep-eval task.
type DeepOutput struct {
	Posting   PostingFacts `json:"posting"`
	Must      []Item       `json:"must_have"`
	Nice      []Item       `json:"nice_to_have"`
	Hidden    []Item       `json:"hidden_signals"`
	Alignment []Item       `json:"profile_alignment"`
	RedFlags  []Flag       `json:"red_flags"`
	Legit     struct {
		Level   string   `json:"level"`
		Signals []string `json:"signals"`
	} `json:"legitimacy"`
	Sections Sections `json:"sections"`
}

// Answer is one drafted application answer.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Status   string `json:"status"`
}

// AnswersOutput is the structured reply of the follow-up call.
type AnswersOutput struct {
	Answers []Answer `json:"answers"`
}

// ScreenOutput is the structured reply of the quick-screen task.
type ScreenOutput struct {
	Score        float64  `json:"score"`
	Decision     string   `json:"decision"`
	Legitimacy   string   `json:"legitimacy"`
	ReviewReason string   `json:"review_reason"`
	Blockers     []string `json:"blockers"`
	Reasons      []string `json:"reasons"`
	Concerns     []string `json:"concerns"`
	Summary      string   `json:"summary"`
}

// modelOnlyKeys are fields the model must not supply; they are dropped and
// logged if present at the top level of a reply.
var modelOnlyKeys = []string{"score", "midpoint", "raw", "cap", "caps", "penalty", "recommendation", "verification", "interval", "documents_eligible"}

func obj(required []string, props string) string {
	req, _ := json.Marshal(required)
	return `{"type":"object","required":` + string(req) + `,"properties":{` + props + `}}`
}

const (
	hitEnum   = `{"type":"number","enum":[0,0.5,1]}`
	strT      = `{"type":"string"}`
	strList   = `{"type":"array","items":{"type":"string"}}`
	itemProps = `"id":` + strT + `,"label":{"type":"string","minLength":1},"source":` + strT + `,"hit_level":` + hitEnum + `,"evidence":` + strT
	gateEnum  = `{"type":"string","enum":["none","work_authorization","security_clearance","education","license","program_eligibility","required_language","experience_threshold","onsite_location"]}`
	legitEnum = `{"type":"string","enum":["credible","unclear","doubtful"]}`
)

func itemList(extra string, minItems int) string {
	return `{"type":"array","minItems":` + strconv.Itoa(minItems) + `,"items":` +
		obj([]string{"id", "label", "hit_level", "evidence"}, itemProps+extra) + `}`
}

// alignmentList is the profile_alignment list: the four dimensions of
// AlignmentIDs, each id from that closed set.
func alignmentList() string {
	ids, _ := json.Marshal(AlignmentIDs)
	props := `"id":{"type":"string","enum":` + string(ids) + `},"label":{"type":"string","minLength":1},"hit_level":` + hitEnum + `,"evidence":` + strT
	return `{"type":"array","minItems":4,"maxItems":4,"items":` + obj([]string{"id", "label", "hit_level", "evidence"}, props) + `}`
}

// DeepSchema is the schema the deep-eval reply must satisfy. Unknown top-level
// fields are allowed so a stray model-supplied score can be ignored and logged.
func DeepSchema() json.RawMessage {
	comp := obj([]string{"text"}, `"text":`+strT+`,"minimum":{"type":["number","null"]},"maximum":{"type":["number","null"]},"currency":`+strT+`,"period":`+strT)
	posting := obj([]string{"title", "company"}, `"title":`+strT+`,"company":`+strT+`,"location":`+strT+
		`,"work_model":{"type":"string","enum":["remote","hybrid","onsite","unknown"]},"compensation":`+comp+
		`,"employment_type":`+strT+`,"company_size":{"type":"string","enum":["1-10","11-50","51-200","201-1000","1001-5000","5000+","unknown"]},"role_family":`+strT)
	flag := obj([]string{"id", "label", "level", "evidence"}, `"id":`+strT+`,"label":`+strT+`,"level":`+hitEnum+`,"evidence":`+strT)
	legit := obj([]string{"level"}, `"level":`+legitEnum+`,"signals":`+strList)
	gap := obj([]string{"id", "kind", "nearest", "mitigation"}, `"id":`+strT+`,"kind":{"type":"string","enum":["eligibility","core","preference"]},"nearest":`+strT+`,"mitigation":`+strT)
	sections := obj([]string{"snapshot"}, `"snapshot":`+strT+`,"level_trajectory":`+strT+`,"pay_notes":`+strT+`,"tailoring_plan":`+strList+`,"interview_prep":`+strList+`,"keywords":`+strList+
		`,"gaps":{"type":"array","items":`+gap+`}`)
	return json.RawMessage(obj([]string{"posting", "must_have", "nice_to_have", "hidden_signals", "profile_alignment", "red_flags", "legitimacy", "sections"},
		`"posting":`+posting+
			`,"must_have":`+itemList(`,"gate_class":`+gateEnum, 1)+
			`,"nice_to_have":`+itemList("", 0)+
			`,"hidden_signals":`+itemList("", 0)+
			`,"profile_alignment":`+alignmentList()+
			`,"red_flags":{"type":"array","items":`+flag+`}`+
			`,"legitimacy":`+legit+`,"sections":`+sections))
}

// ScreenSchema is the schema the quick-screen reply must satisfy.
func ScreenSchema() json.RawMessage {
	return json.RawMessage(obj([]string{"score", "decision", "legitimacy", "blockers", "summary"},
		`"score":{"type":"number","minimum":0,"maximum":5},"decision":{"type":"string","enum":["advance","drop","review"]},`+
			`"legitimacy":`+legitEnum+`,"review_reason":`+strT+`,"blockers":`+strList+`,"reasons":`+strList+`,"concerns":`+strList+`,"summary":`+strT))
}

// ExtractSchema is the schema of the jd-extract reply.
func ExtractSchema() json.RawMessage {
	return json.RawMessage(obj([]string{"title", "company", "location"}, `"title":`+strT+`,"company":`+strT+`,"location":`+strT))
}

// AnswersSchema is the schema of the follow-up answers reply.
func AnswersSchema() json.RawMessage {
	answer := obj([]string{"question", "answer", "status"}, `"question":`+strT+`,"answer":`+strT+`,"status":{"type":"string","enum":["answered","unanswered"]}`)
	return json.RawMessage(obj([]string{"answers"}, `"answers":{"type":"array","items":`+answer+`}`))
}

package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/identity"
)

// ErrRefused marks a start the service declined for a reason the model can
// act on (a full queue, a scan already running). The tool answers with the
// message instead of failing.
var ErrRefused = errors.New("refused")

// refusedf builds an error the tools turn into an answer.
func refusedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// answerRefusal turns a refusal into a result and leaves other errors alone. A
// refusal returns nothing the service read, only the product's reason, so it
// is clean: it does not taint the turn. So is the run-start line a tool writes
// itself: the run's own output reaches the conversation later, marked.
func answerRefusal(err error) (ToolResult, error) {
	if errors.Is(err, ErrRefused) {
		return ToolResult{Text: strings.TrimPrefix(err.Error(), ErrRefused.Error()+": "), Clean: true}, nil
	}
	return ToolResult{}, err
}

// startTimeout is how long a tool that starts a background run may take to
// hand back the run id.
const startTimeout = 5 * time.Second

// EvaluationRequest is one posting the evaluate tool queues.
type EvaluationRequest struct {
	URL, Text, Title, Company string
	ConversationID            string
}

// Evaluations queues an evaluation on the runs board and returns its run id.
type Evaluations interface {
	StartEvaluation(ctx context.Context, req EvaluationRequest) (runID string, err error)
}

// ScanRequest names one source of the discovery catalog.
type ScanRequest struct {
	Source         string
	ConversationID string
}

// Scans starts a discovery scan on the runs board and returns its run id.
// ScanConflict returns the refusal a scan of the source would meet now (one
// is already running), or nil.
type Scans interface {
	StartScan(ctx context.Context, req ScanRequest) (runID string, err error)
	ScanConflict(ctx context.Context, source string) error
}

// RegistrationPlan is what the apply chain would do to sign up at a
// destination, read from the account policy and the credential index. Nothing
// is created, typed or sent to produce it.
type RegistrationPlan struct {
	Host, System string
	// Supported is false when no sign-up flow exists for the destination.
	Supported bool
	// HasAccount says a stored sign-in already exists for the destination.
	HasAccount bool
	// GateAllows and GateReason are the account creation gate's answer for a
	// run the user starts now, whether it submits or only fills.
	GateAllows bool
	GateReason string
}

// RegistrationPlanner reads the plan for one destination.
type RegistrationPlanner interface {
	PlanRegistration(ctx context.Context, destination string) (RegistrationPlan, error)
}

// ResumeRequest is one tailored résumé the tailor tool queues.
type ResumeRequest struct {
	CandidacyID int64
	CoverLetter bool
	PDF         bool
	// ConversationID is told when the documents are ready.
	ConversationID string
}

// Resumes queues a document run for an application and returns its run id.
type Resumes interface {
	StartResume(ctx context.Context, req ResumeRequest) (runID string, err error)
}

// ActionTools builds the tools that start background runs or read the plan of
// one, for the services the build has.
func ActionTools(d ToolDeps) []Tool {
	var out []Tool
	if d.Evaluations != nil {
		out = append(out, evaluateJob(d.Evaluations))
	}
	if d.Scans != nil {
		out = append(out, scanPortal(d.Scans))
	}
	if d.Registrations != nil {
		out = append(out, prepareRegistration(d.Registrations))
	}
	if d.Resumes != nil {
		out = append(out, tailorResume(d.Resumes))
	}
	return out
}

// publicPostingURL checks that a URL is one the system may fetch for the user.
func publicPostingURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("the url must be an absolute http or https URL")
	}
	if u.User != nil {
		return errors.New("the url must not carry user-info")
	}
	if why := identity.NonPublicHost(u.Hostname()); why != "" {
		return fmt.Errorf("the url points at a host that is not on the public internet (%s)", why)
	}
	return nil
}

func evaluateJob(e Evaluations) Tool {
	return Tool{Name: "evaluate_job", Label: "Starting an evaluation", Class: ClassExternalAction, OutsideContent: true, PII: PIILow, Timeout: startTimeout,
		Description: "Queue an evaluation of one job posting: give its URL (or its text). The evaluation runs in the background on the Runs page and scores the posting against the user's profile; this call returns the run id. Use it when the user asks to evaluate or score a posting.",
		Schema: `{"type":"object","additionalProperties":false,"properties":{
			"url":{"type":"string","maxLength":2000,"description":"The public posting URL."},
			"text":{"type":"string","maxLength":20000,"description":"The posting text, when the user pasted it instead of a link."},
			"title":{"type":"string","maxLength":200},
			"company":{"type":"string","maxLength":200}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ URL, Text, Title, Company string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			a.URL, a.Text = strings.TrimSpace(a.URL), strings.TrimSpace(a.Text)
			if a.URL == "" && a.Text == "" {
				return ToolResult{Text: "Refused: give the posting URL or its text.", Clean: true}, nil
			}
			if a.URL != "" {
				if err := publicPostingURL(a.URL); err != nil {
					return ToolResult{Text: "Refused: " + err.Error() + ".", Clean: true}, nil
				}
			}
			id, err := e.StartEvaluation(ctx, EvaluationRequest{URL: a.URL, Text: a.Text, Title: a.Title, Company: a.Company, ConversationID: sessionOf(c)})
			if err != nil {
				return answerRefusal(err)
			}
			return ToolResult{RunID: id, Text: fmt.Sprintf("Queued the evaluation (run %s). Follow it on the Runs page; the report appears there when it finishes.", id), Clean: true}, nil
		}}
}

func scanSourceEnum() string {
	names := make([]string, len(discovery.Sources))
	for i, s := range discovery.Sources {
		names[i] = `"` + s.Name + `"`
	}
	return "[" + strings.Join(names, ",") + "]"
}

func scanPortal(s Scans) Tool {
	return Tool{Name: "scan_portal", Label: "Starting a scan", Class: ClassExternalAction, OutsideContent: true, PII: PIILow, Timeout: startTimeout,
		Description: "Start a discovery scan of one job source. The scan runs in the background on the Runs page and fills the pipeline; this call returns the run id. One scan per source runs at a time.",
		Schema:      `{"type":"object","additionalProperties":false,"required":["source"],"properties":{"source":{"type":"string","enum":` + scanSourceEnum() + `}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Source string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			id, err := s.StartScan(ctx, ScanRequest{Source: a.Source, ConversationID: sessionOf(c)})
			if err != nil {
				return answerRefusal(err)
			}
			return ToolResult{RunID: id, Text: fmt.Sprintf("Started the %s scan (run %s). Follow it on the Runs page; new postings land in the pipeline.", a.Source, id), Clean: true}, nil
		},
		Conflict: func(ctx context.Context, c ToolCall) error {
			var a struct{ Source string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return err
			}
			return s.ScanConflict(ctx, a.Source)
		}}
}

func prepareRegistration(p RegistrationPlanner) Tool {
	return Tool{Name: "prepare_registration", Label: "Planning a registration", Class: ClassExternalAction, RequiresApproval: true, Idempotent: true, PII: PIIHigh, Timeout: readTimeout,
		Description: "Read the plan for signing up at one exact destination URL: whether a sign-up flow exists, whether a cooldown would hold it, and whether an account is already stored. It creates nothing and types nothing; an account is created only inside an application run the user started, whether it submits or only fills. The user approves the exact destination on a card first.",
		Schema:      `{"type":"object","additionalProperties":false,"required":["url"],"properties":{"url":{"type":"string","maxLength":2000,"description":"The exact sign-up destination."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ URL string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			if err := publicPostingURL(a.URL); err != nil {
				return ToolResult{Text: "Refused: " + err.Error() + "."}, nil
			}
			plan, err := p.PlanRegistration(ctx, strings.TrimSpace(a.URL))
			if err != nil {
				return answerRefusal(err)
			}
			return ToolResult{Text: registrationText(plan)}, nil
		}}
}

func registrationText(p RegistrationPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Destination %s (%s).\n", p.Host, cmp.Or(p.System, "unknown system"))
	switch {
	case !p.Supported:
		b.WriteString("No sign-up flow exists for this destination, so the chain would not create an account here.\n")
	case p.GateAllows:
		b.WriteString("The account creation gate would allow a sign-up in an application run the user starts now.\n")
	default:
		fmt.Fprintf(&b, "The account creation gate would stop a sign-up now: %s\n", p.GateReason)
	}
	if p.HasAccount {
		b.WriteString("A sign-in for this destination is already stored, so the chain would sign in instead of signing up.\n")
	}
	b.WriteString("Nothing was created. An account is created only inside an application run the user starts, whether it submits or only fills.")
	return b.String()
}

func tailorResume(r Resumes) Tool {
	return Tool{Name: "tailor_resume", Label: "Starting a tailored resume", Class: ClassWriteLocal, PII: PIIHigh, Timeout: startTimeout,
		Description: "Queue a tailored resume (and optionally its cover letter) for one application, by application_id. It runs in the background on the Runs page and stores a new document version; this call returns the run id.",
		Schema: `{"type":"object","additionalProperties":false,"required":["application_id"],"properties":{
			"application_id":{"type":"integer","minimum":1,"description":"The application's id in the tracker."},
			"include_cover_letter":{"type":"boolean"},
			"pdf":{"type":"boolean","description":"Also compile and verify the PDF."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				ApplicationID      int64 `json:"application_id"`
				IncludeCoverLetter bool  `json:"include_cover_letter"`
				PDF                bool
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			id, err := r.StartResume(ctx, ResumeRequest{CandidacyID: a.ApplicationID, CoverLetter: a.IncludeCoverLetter, PDF: a.PDF, ConversationID: sessionOf(c)})
			if err != nil {
				return answerRefusal(err)
			}
			what := "tailored resume"
			if a.IncludeCoverLetter {
				what = "tailored resume and cover letter"
			}
			return ToolResult{RunID: id, Text: fmt.Sprintf("Queued the %s for application %d (run %s). Follow it on the Runs page.", what, a.ApplicationID, id)}, nil
		}}
}

// sessionOf is the conversation a call belongs to, for the run it starts.
func sessionOf(c ToolCall) string {
	if c.Turn == nil {
		return ""
	}
	return c.Turn.SessionID
}

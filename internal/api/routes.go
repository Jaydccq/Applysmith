package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
)

// Access is who may call a route.
type Access string

const (
	AccessPublic   Access = "public"
	AccessStandard Access = "standard"
	AccessAdmin    Access = "admin"
	// AccessAgentProcess is the credential of one running pi process: it is
	// accepted on the internal agent routes and on nothing else, and no user
	// credential is accepted there.
	AccessAgentProcess Access = "agent-process"
)

// Remote says whether a route is served on remote listeners.
type Remote string

const (
	RemoteAllowed Remote = "allowed"
	RemoteDenied  Remote = "denied"
)

// Body is the request body class of a route.
type Body string

const (
	BodyNone   Body = "none"
	BodyJSON   Body = "json"
	BodyUpload Body = "upload"
	// BodyJSONLarge is a json body of up to 4 MiB, for the documents that
	// outgrow the json limit: the answer bank and the career documents of the
	// evidence import.
	BodyJSONLarge Body = "json-large"
	// BodyModel is a model request of a pi process, up to agent.MaxModelBody,
	// passed to the gateway as it came.
	BodyModel Body = "model"
)

// Payload names that are not wire structs.
const (
	PayloadNone   = "none"
	PayloadEvents = "events"
	PayloadAsset  = "asset"
	// PayloadModel is a provider's request or streamed reply, in the
	// provider's own format.
	PayloadModel = "model"
)

// Route is one entry of the route table. A route that is not in the table is
// not reachable, because the table is the only way a handler gets registered.
type Route struct {
	Method      string
	Path        string
	Access      Access
	Remote      Remote
	Limit       Limit
	Body        Body
	Secret      bool // returns or accepts secret values
	SessionOnly bool // accepts only the dashboard session cookie
	Request     string
	Response    string
	// Deadline replaces the server's synchronous handler deadline when set.
	Deadline time.Duration
}

// streams reports a route whose reply is a stream that outlives the handler
// deadline: an event stream, or a model reply relayed as the provider sends it.
func (r Route) streams() bool { return r.Response == PayloadEvents || r.Response == PayloadModel }

// Pattern is the registration form understood by the router.
func (r Route) Pattern() string { return r.Method + " " + r.Path }

const (
	patternHealth = "GET /api/health"
	patternStatic = "GET /{path...}"
)

// route builds a table entry for the common case: a standard-class route,
// denied on remote listeners, in the general limit class.
func route(method, path string, mod func(*Route)) Route {
	r := Route{Method: method, Path: path, Access: AccessStandard, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone,
		Request: PayloadNone, Response: PayloadNone}
	mod(&r)
	return r
}

func reads(response string) func(*Route) { return func(r *Route) { r.Response = response } }

// remoteReads is reads for a route the remote allowlist contains.
func remoteReads(response string) func(*Route) {
	return func(r *Route) { r.Response = response; r.Remote = RemoteAllowed }
}

func sends(request, response string) func(*Route) {
	return func(r *Route) { r.Body, r.Request, r.Response = BodyJSON, request, response }
}

// sendsLarge is sends for a body of up to 4 MiB.
func sendsLarge(request, response string) func(*Route) {
	return func(r *Route) { r.Body, r.Request, r.Response = BodyJSONLarge, request, response }
}

// coreRoutes are the server's own routes: health, the board stream, the
// credentials and the live backup.
var coreRoutes = []Route{
	{Method: "GET", Path: "/api/health", Access: AccessPublic, Remote: RemoteAllowed, Limit: LimitGeneral, Body: BodyNone,
		Request: PayloadNone, Response: "Health"},
	{Method: "GET", Path: "/api/board/events", Access: AccessStandard, Remote: RemoteAllowed, Limit: LimitGeneral, Body: BodyNone,
		Request: PayloadNone, Response: PayloadEvents},
	{Method: "POST", Path: "/api/pairing/codes", Access: AccessAdmin, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone, Secret: true,
		Request: PayloadNone, Response: "PairingCode"},
	{Method: "POST", Path: "/api/pairing/claim", Access: AccessPublic, Remote: RemoteDenied, Limit: LimitPairing, Body: BodyJSON, Secret: true,
		Request: "PairingClaim", Response: "TokenIssued"},
	{Method: "POST", Path: "/api/launch-codes", Access: AccessAdmin, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone, Secret: true,
		Request: PayloadNone, Response: "LaunchCode"},
	{Method: "POST", Path: "/api/token/rotate", Access: AccessAdmin, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone, Secret: true,
		Request: PayloadNone, Response: "TokenIssued"},
	{Method: "POST", Path: "/api/backups", Access: AccessAdmin, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyJSON,
		Request: "BackupRequest", Response: "BackupOperation"},
	{Method: "GET", Path: "/api/backups/{id}", Access: AccessAdmin, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone,
		Request: PayloadNone, Response: "BackupOperation"},
}

// gmailSignInDeadline is the wait of a route that signs in to Gmail: the
// sign-in, the folder list and a search, each bounded by gmail.RequestTimeout.
const gmailSignInDeadline = time.Minute

// modelDeadline is the wait of a route that has a model answer in the request
// (a draft, a composed answer, a summary). The page waits
// MODEL_TIMEOUT_MS (web/src/api/client.ts), which a test holds above this.
const modelDeadline = 2 * time.Minute

// draftRoute is a message draft of the dashboard session: one model call.
func draftRoute(request string) func(*Route) {
	return func(r *Route) {
		sends(request, "MessageDraft")(r)
		r.SessionOnly, r.Limit, r.Deadline = true, LimitEvaluation, modelDeadline
	}
}

// trackerRoutes serve the application tracker and the read-only apply list.
// Every change is made as the actor the credential implies; the bulk change
// accepts the dashboard session only.
var trackerRoutes = []Route{
	route("GET", "/api/candidacies", remoteReads("CandidacyPage")),
	route("POST", "/api/candidacies", sends("CandidacyCreate", "Candidacy")),
	route("POST", "/api/candidacies/bulk-status", func(r *Route) { sends("BulkStatusRequest", "BulkStatusResult")(r); r.SessionOnly = true }),
	route("POST", "/api/candidacies/bulk-archive", func(r *Route) { sends("BulkArchiveRequest", "BulkArchiveResult")(r); r.SessionOnly = true }),
	route("GET", "/api/candidacies/{id}", remoteReads("CandidacyDetail")),
	route("PATCH", "/api/candidacies/{id}", sends("StatusChangeRequest", "StatusChangeResult")),
	route("POST", "/api/candidacies/{id}/notes", sends("NoteRequest", "Candidacy")),
	route("POST", "/api/candidacies/{id}/followup", sends("FollowupRequest", "Candidacy")),
	route("POST", "/api/candidacies/{id}/followup-draft", draftRoute("FollowupDraftRequest")),
	route("POST", "/api/candidacies/{id}/outreach-draft", draftRoute("OutreachDraftRequest")),
	route("POST", "/api/candidacies/{id}/documents-override", func(r *Route) { reads("Candidacy")(r); r.SessionOnly = true }),
	route("POST", "/api/candidacies/{id}/confirm-submitted", func(r *Route) { reads("Candidacy")(r); r.SessionOnly = true }),
	route("POST", "/api/candidacies/{id}/archive", reads("Candidacy")),
	route("POST", "/api/candidacies/{id}/unarchive", reads("Candidacy")),
	route("GET", "/api/candidacies/{id}/transitions", reads("CandidacyTransitions")),
	route("GET", "/api/tracker/summary", reads("TrackerSummary")),
	route("GET", "/api/tracker/followups", reads("FollowupList")),
	route("GET", "/api/tracker/outcomes", reads("TrackerOutcomes")),
	route("GET", "/api/tracker/export", reads(PayloadAsset)),
	route("GET", "/api/tracker/duplicates", reads("TrackerDuplicates")),
	route("POST", "/api/tracker/duplicates/merge", func(r *Route) { sends("DuplicateMergeRequest", "DuplicateMergeResult")(r); r.SessionOnly = true }),
	route("POST", "/api/tracker/duplicates/dismiss", func(r *Route) { sends("DuplicateDismissal", "TrackerDuplicates")(r); r.SessionOnly = true }),
	route("GET", "/api/applications/lookup", remoteReads("ApplicationLookup")),
	route("GET", "/api/apply/list", remoteReads("ApplyList")),
	route("GET", "/api/apply/skip-status", reads("ApplySkipStatus")),
	route("GET", "/api/apply/skip-list", reads("ApplySkipList")),
	route("PUT", "/api/apply/skip-list", sends("ApplySkipListWrite", "ApplySkipList")),
	route("POST", "/api/apply/runs", sends("ApplyRunRequest", "RunAccepted")),
	route("GET", "/api/apply/runs/{id}", reads("ApplyRunView")),
	route("POST", "/api/apply/runs/{id}/retry", sends("ApplyRetryRequest", "RunAccepted")),
	route("POST", "/api/apply/runs/{id}/answers", sends("ApplyAnswerRequest", "ApplyAnswerSaved")),
	route("POST", "/api/apply/runs/{id}/submit", sends("ApplySubmitRequest", "RunAccepted")),
	route("POST", "/api/apply/runs/{id}/takeover", func(r *Route) { reads("TakeoverStarted")(r); r.SessionOnly = true }),
	route("POST", "/api/apply/runs/{id}/show-tab", reads("ApplyTabShown")),
	route("POST", "/api/apply/batches", sends("ApplyBatchRequest", "ApplyBatchAccepted")),
	route("GET", "/api/apply/batches/{id}", reads("ApplyBatchView")),
	route("POST", "/api/autofill/active-tab", reads("RunAccepted")),
	route("GET", "/api/autofill/active-tab/{id}", reads("AutofillTabResult")),
}

// evaluationRoutes serve evaluation jobs, the run records and streams every
// long operation shares, and the report catalog. Starting an evaluation is in
// the evaluation limit class.
var evaluationRoutes = []Route{
	route("POST", "/api/evaluations", func(r *Route) { sends("EvaluationRequest", "RunAccepted")(r); r.Limit = LimitEvaluation }),
	route("POST", "/api/evaluations/gaps", sends("GapRequest", "RunAccepted")),
	route("GET", "/api/evaluations/gaps", reads("GapList")),
	route("GET", "/api/evaluations/gaps/{id}", reads("GapReport")),
	route("POST", "/api/evaluations/gaps/{id}/confirm", sends("GapConfirmRequest", "GapReport")),
	route("POST", "/api/evaluations/gaps/{id}/plan", sends("GapPlanRequest", "GapReport")),
	route("POST", "/api/evaluations/gaps/{id}/progress", sends("GapProgressRequest", "GapReport")),
	route("GET", "/api/evaluations/{id}", reads("EvaluationView")),
	route("POST", "/api/evaluations/{id}/retry", func(r *Route) { reads("RunAccepted")(r); r.Limit = LimitEvaluation }),
	route("GET", "/api/runs/{id}", remoteReads("RunView")),
	route("GET", "/api/runs/{id}/events", remoteReads(PayloadEvents)),
	route("POST", "/api/runs/{id}/cancel", remoteReads("RunView")),
	route("GET", "/api/reports", remoteReads("ReportPage")),
	route("GET", "/api/reports/{number}", remoteReads("ReportDetail")),
	route("GET", "/api/reports/{number}/html", reads(PayloadAsset)),
}

// discoveryRoutes serve scans, the pipeline inbox and blocked companies.
var discoveryRoutes = []Route{
	route("POST", "/api/scans", sends("ScanRequest", "RunAccepted")),
	route("GET", "/api/scans", reads("ScanList")),
	route("GET", "/api/scans/{id}", reads("ScanRecord")),
	route("GET", "/api/scan-sources", reads("ScanSourceList")),
	route("GET", "/api/discovery/funnel", reads("SourceFunnel")),
	route("GET", "/api/discovery/funnel/week", reads("FunnelWeek")),
	route("GET", "/api/inbox", reads("InboxPage")),
	route("POST", "/api/inbox/promote", sends("InboxPromoteRequest", "InboxPromoteResult")),
	route("POST", "/api/inbox/{id}/dismiss", reads("InboxRow")),
	route("POST", "/api/inbox/{id}/restore", reads("InboxRow")),
	route("GET", "/api/leads", reads("LeadPage")),
	route("POST", "/api/leads/{id}/dismiss", reads("Lead")),
	route("POST", "/api/leads/{id}/restore", reads("Lead")),
	route("POST", "/api/leads/expand", sends("LeadRunRequest", "RunAccepted")),
	route("POST", "/api/leads/evaluate", sends("LeadRunRequest", "RunAccepted")),
	route("GET", "/api/portals/companies", reads("PortalCompanyList")),
	route("POST", "/api/portals/detect", sends("PortalDetectRequest", "PortalDetection")),
	route("GET", "/api/portals/target-roles", reads("PortalTargetRoles")),
	route("GET", "/api/registry", reads("PortalRegistry")),
	route("POST", "/api/registry/seed", reads("RunAccepted")),
	route("GET", "/api/blocked-companies", reads("BlockedCompanyList")),
	route("POST", "/api/blocked-companies", sends("BlockedCompanyRequest", "BlockedCompany")),
	route("DELETE", "/api/blocked-companies", reads("BlockedCompanyList")),
}

// signalRoutes serve the hiring signals: the list, the evidence chain of a
// company, the review queue, the user's actions and the ticks. None is in the
// remote allowlist.
var signalRoutes = []Route{
	route("GET", "/api/signals", reads("SignalList")),
	route("GET", "/api/signals/status", reads("SignalStatus")),
	route("GET", "/api/signals/review", reads("SignalReviewList")),
	route("POST", "/api/signals/review/{id}/approve", reads("SignalReviewItem")),
	route("POST", "/api/signals/review/{id}/reject", reads("SignalReviewItem")),
	route("POST", "/api/signals/review/{id}/restore", reads("SignalReviewItem")),
	route("POST", "/api/signals/ticks", sends("SignalTickRequest", "RunAccepted")),
	route("POST", "/api/signals/rescore", reads("SignalRescoreResult")),
	route("GET", "/api/signals/{companyId}", reads("SignalDetail")),
	route("POST", "/api/signals/{companyId}/watch", reads("SignalRow")),
	route("POST", "/api/signals/{companyId}/dismiss", reads("SignalRow")),
	route("POST", "/api/signals/{companyId}/restore", reads("SignalRow")),
	route("POST", "/api/signals/{companyId}/lift-block", reads("SignalRow")),
	route("POST", "/api/signals/{companyId}/evaluate-postings", reads("SignalEvaluateResult")),
}

// llmRoutes serve the Models settings. Storing or clearing a key is an admin
// action on a secret.
var llmRoutes = []Route{
	route("GET", "/api/llm/routing", reads("LLMRouting")),
	route("PATCH", "/api/llm/routing", sends("LLMRoutingPatch", "LLMRouting")),
	route("POST", "/api/llm/preset", sends("LLMPresetRequest", "LLMRouting")),
	route("PUT", "/api/llm/prices/{provider}", func(r *Route) { sends("LLMPriceRequest", "LLMRouting")(r); r.Access = AccessAdmin }),
	route("PUT", "/api/llm/providers/{provider}", func(r *Route) { sends("LLMProviderRequest", "LLMRouting")(r); r.Access = AccessAdmin }),
	route("DELETE", "/api/llm/providers/{provider}", func(r *Route) { reads("LLMRouting")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("GET", "/api/llm/accounts", reads("ProviderAccounts")),
	route("PUT", "/api/llm/accounts/{id}/credentials", func(r *Route) {
		sends("ProviderCredentialRequest", "ProviderAccount")(r)
		r.Access, r.Secret = AccessAdmin, true
	}),
	route("DELETE", "/api/llm/accounts/{id}/credentials/{kind}", func(r *Route) { reads("ProviderAccount")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("POST", "/api/llm/accounts/{id}/refresh", func(r *Route) { reads("ProviderAccount")(r); r.Access = AccessAdmin }),
	route("PUT", "/api/llm/keys/{provider}", func(r *Route) {
		sends("LLMKeyRequest", "LLMKeyStatus")(r)
		r.Access, r.Secret = AccessAdmin, true
	}),
	route("DELETE", "/api/llm/keys/{provider}", func(r *Route) { reads("LLMKeyStatus")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("GET", "/api/llm/chatgpt", reads("ChatGPTStatus")),
	route("POST", "/api/llm/chatgpt/signin", func(r *Route) { reads("ChatGPTSignInStarted")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("POST", "/api/llm/chatgpt/signout", func(r *Route) { sends("ChatGPTSignOutRequest", "ChatGPTSignOutResult")(r); r.Access = AccessAdmin }),
	route("GET", "/api/llm/chatgpt/models", reads("ChatGPTModels")),
	route("POST", "/api/llm/providers/{provider}/test", func(r *Route) { sends("LLMTestRequest", "LLMTestStarted")(r); r.Limit = LimitEvaluation }),
	route("GET", "/api/llm/cost", reads("LLMCost")),
	route("GET", "/api/llm/cost/daily", reads("UsageDays")),
	route("GET", "/api/llm/calls", reads("UsageCallPage")),
	route("GET", "/api/llm/calls/export", reads(PayloadAsset)),
	route("GET", "/api/llm/traces", reads("UsageTraceList")),
	route("GET", "/api/llm/traces/{id}", reads("UsageTraceDetail")),
}

// systemRoutes serve the Files and Advanced sections of Settings: where the
// workspace is, the doctor, the redacted log and the diagnostics export. None
// is available to a remote device.
var systemRoutes = []Route{
	route("GET", "/api/system/info", reads("SystemInfo")),
	route("GET", "/api/system/logs", reads("LogLines")),
	route("GET", "/api/system/diagnostics", reads(PayloadAsset)),
}

// knowledgeRoutes serve evidence retrieval and the story coverage views.
var knowledgeRoutes = []Route{
	route("GET", "/api/kb/stories/coverage", reads("KBCoverage")),
	route("GET", "/api/kb/github", reads("KBGitHubAccount")),
	route("POST", "/api/kb/github/signin", reads("KBGitHubSignIn")),
	route("DELETE", "/api/kb/github", reads("KBGitHubAccount")),
	route("GET", "/api/kb/github/repos", reads("KBGitHubRepos")),
	route("GET", "/api/kb/sources", reads("KBRepoList")),
	route("POST", "/api/kb/sources", sends("KBRepoAdd", "KBRepo")),
	route("POST", "/api/kb/sources/{id}/ingest", sends("KBIngestRequest", "RunAccepted")),
	route("DELETE", "/api/kb/sources/{id}", reads("KBUndo")),
	route("GET", "/api/kb/stories/mining", reads("KBMiningList")),
	route("POST", "/api/kb/stories/mining", func(r *Route) { sends("KBMiningStart", "KBMiningSession")(r); r.Deadline = modelDeadline }),
	route("GET", "/api/kb/stories/mining/suggestions", reads("KBQuadrantSuggestions")),
	route("GET", "/api/kb/stories/mining/{id}", reads("KBMiningSession")),
	route("POST", "/api/kb/stories/mining/{id}/replies", sends("KBMiningReply", "KBMiningSession")),
	route("POST", "/api/kb/stories/mining/{id}/save", reads("KBDraft")),
	route("DELETE", "/api/kb/stories/mining/{id}", reads("KBUndo")),
	route("POST", "/api/kb/imports", func(r *Route) { r.Body, r.Response = BodyUpload, "KBDraft" }),
	route("POST", "/api/kb/drafts/{id}/publish", reads("KBDocument")),
	route("GET", "/api/kb/drafts", reads("KBDraftList")),
	route("PATCH", "/api/kb/drafts/{id}", sends("KBDraftChange", "KBDraft")),
	route("POST", "/api/kb/drafts/{id}/confirm", sends("KBDraftChange", "KBDraft")),
	route("DELETE", "/api/kb/drafts/{id}", reads("KBUndo")),
	route("GET", "/api/kb/documents", reads("KBDocumentList")),
	route("POST", "/api/kb/documents", sends("KBSnippetCreate", "KBDocument")),
	route("GET", "/api/kb/documents/{id}", reads("KBDocument")),
	route("PATCH", "/api/kb/documents/{id}", sends("KBDocumentRevise", "KBDocument")),
	route("GET", "/api/kb/documents/{id}/previous", reads("KBPrevious")),
	route("DELETE", "/api/kb/documents/{id}", reads("KBUndo")),
	route("POST", "/api/kb/restore", sends("KBUndo", "KBRestored")),
	route("POST", "/api/kb/briefs", sends("KBBriefRequest", "RunAccepted")),
	route("POST", "/api/kb/stories", sends("KBStoryCreate", "KBStorySaved")),
	route("GET", "/api/kb/stories/proposals", reads("KBStoryProposals")),
	route("POST", "/api/kb/stories/proposals", func(r *Route) { reads("RunAccepted")(r); r.Limit = LimitEvaluation }),
	route("POST", "/api/kb/stories/proposals/dismiss", sends("KBStoryProposalRef", "KBStoryProposals")),
	route("POST", "/api/kb/stories/proposals/restore", sends("KBStoryProposalRef", "KBStoryProposals")),
}

// configRoutes serve the files the profile page edits: read with a revision,
// replace against it, or change the profile by key path, and list the earlier
// contents a save kept. A résumé upload stores
// the PDF; the roster entry that names it is a profile edit.
var configRoutes = []Route{
	route("GET", "/api/config/cv/draft", reads("CVDraft")),
	route("POST", "/api/config/cv/draft/dismiss", sends("CVDraftDismiss", "CVDraft")),
	route("GET", "/api/config/policy", reads("ConfigDocument")),
	route("PATCH", "/api/config/policy", func(r *Route) { sends("ConfigPatch", "ConfigSaved")(r); r.SessionOnly = true }),
	route("PUT", "/api/config/policy", func(r *Route) { sendsLarge("ConfigSave", "ConfigSaved")(r); r.SessionOnly = true }),
	route("GET", "/api/config/{file}", reads("ConfigDocument")),
	route("GET", "/api/config/{file}/backups", reads("ConfigBackupList")),
	route("PUT", "/api/config/{file}", sendsLarge("ConfigSave", "ConfigSaved")),
	route("PATCH", "/api/config/{file}", sends("ConfigPatch", "ConfigSaved")),
	route("POST", "/api/resumes", func(r *Route) { r.Body, r.Response = BodyUpload, "ResumeUploaded" }),
}

// answerBankRoutes serve the whole bank (read as the file holds it, replaced
// against a revision), the needs-answer view, the preview and
// apply of the profile-derived entries, and the runtime settings. Writes name
// ids or flags only; no value is taken from the client. The transcript routes
// read, store (an uploaded PDF, in place of the one before) and remove the
// academic transcript an application run attaches. The learning routes
// take uploads of events (the one place a value arrives, and the server decides
// what it keeps), list and delete the manual-fill records, and list, generate,
// approve and reject candidates, or take either back. Approval is an explicit
// call: no route and no process approves one on its own; the user's own answer
// may come with it.
var answerBankRoutes = []Route{
	route("GET", "/api/answer-bank", reads("AnswerBankFile")),
	route("PUT", "/api/answer-bank", sendsLarge("AnswerBankSave", "AnswerBankSaved")),
	route("GET", "/api/answer-bank/unanswered", reads("AnswerBankUnanswered")),
	route("GET", "/api/answer-bank/settings", reads("AnswerBankSettings")),
	route("PATCH", "/api/answer-bank/settings", sends("AnswerBankSettingsPatch", "AnswerBankSettings")),
	route("GET", "/api/answer-bank/transcript", reads("AnswerBankTranscript")),
	route("POST", "/api/answer-bank/transcript", func(r *Route) { r.Body, r.Response = BodyUpload, "AnswerBankTranscript" }),
	route("DELETE", "/api/answer-bank/transcript", reads("AnswerBankTranscript")),
	route("POST", "/api/answer-bank/events", sends("LearningEventsRequest", "LearningEventsResult")),
	route("POST", "/api/answer-bank/system-writes", sends("SystemWriteRequest", "SystemWriteRecorded")),
	route("GET", "/api/answer-bank/manual-fills", reads("ManualFillList")),
	route("DELETE", "/api/answer-bank/manual-fills/{id}", reads("ManualFillDeleted")),
	route("POST", "/api/answer-bank/manual-fills/{id}/keep", reads("AnswerBankSaved")),
	route("POST", "/api/answer-bank/manual-fills/{id}/leave", reads("AnswerEdit")),
	route("POST", "/api/answer-bank/manual-fills/{id}/undo", reads("AnswerBankSaved")),
	route("GET", "/api/answer-bank/candidates", reads("AnswerCandidateList")),
	route("POST", "/api/answer-bank/candidates/generate", sends("AnswerCandidateGenerate", "AnswerCandidateGenerateResult")),
	route("POST", "/api/answer-bank/candidates/{id}/approve", sends("AnswerCandidateApprove", "AnswerCandidateDecision")),
	route("POST", "/api/answer-bank/candidates/{id}/unapprove", reads("AnswerCandidateDecision")),
	route("POST", "/api/answer-bank/candidates/{id}/reject", reads("AnswerCandidateDecision")),
	route("POST", "/api/answer-bank/candidates/{id}/restore", reads("AnswerCandidateDecision")),
	route("GET", "/api/answer-bank/usage", reads("AnswerBankUsage")),
	route("POST", "/api/answer-bank/match", func(r *Route) { sendsLarge("AnswerMatchRequest", "AnswerMatchResult")(r); r.Limit = LimitEvaluation }),
}

// answerRoutes serve the model-assisted answer drafts. A draft is a response,
// never a write; the route is standard access, denied on remote listeners and
// in the evaluation limit class because it makes a model call.
var answerRoutes = []Route{
	route("POST", "/api/answers/compose", func(r *Route) {
		sends("AnswerComposeRequest", "AnswerComposeResult")(r)
		r.Limit, r.Deadline = LimitEvaluation, modelDeadline
	}),
}

// documentRoutes serve the application documents: runs that return a run id at
// once, the stored versions, and the workspace defaults. All are standard
// access, remote denied and in the general limit class.
var documentRoutes = []Route{
	route("POST", "/api/documents/resume", sends("DocumentResumeRequest", "RunAccepted")),
	route("POST", "/api/documents/cover-letter", sends("DocumentCoverRequest", "RunAccepted")),
	route("POST", "/api/documents/{id}/pdf", sends("DocumentPDFRequest", "RunAccepted")),
	route("POST", "/api/documents/{id}/edit", sends("DocumentEditRequest", "RunAccepted")),
	route("POST", "/api/documents/{id}/refine", sends("DocumentRefineRequest", "RunAccepted")),
	route("GET", "/api/documents/refine/{id}", reads("DocumentRefinement")),
	route("POST", "/api/documents/{id}/refine/accept", sends("DocumentRefineDecision", "RunAccepted")),
	route("POST", "/api/documents/{id}/refine/discard", sends("DocumentRefineDecision", "DocumentRefinement")),
	route("GET", "/api/documents/source/{id}", reads("DocumentSource")),
	route("GET", "/api/documents/pdf/{id}", reads(PayloadAsset)),
	route("GET", "/api/documents", reads("DocumentList")),
	route("DELETE", "/api/documents/{id}", reads("DocumentList")),
	route("GET", "/api/documents/runs/{id}", reads("DocumentRunView")),
	route("GET", "/api/documents/preferences", reads("DocumentPreferences")),
	route("PATCH", "/api/documents/preferences", sends("DocumentPreferencesPatch", "DocumentPreferences")),
	route("GET", "/api/candidacies/{id}/interview", reads("InterviewWorkspace")),
	route("GET", "/api/interviews/soon", reads("InterviewsSoon")),
	route("POST", "/api/candidacies/{id}/interview/stages", sends("InterviewStageRequest", "InterviewStage")),
	route("GET", "/api/candidacies/{id}/interview/stages/{stage}/pack", reads("InterviewPack")),
	route("POST", "/api/candidacies/{id}/interview/stages/{stage}/pack", sends("InterviewPackRequest", "RunAccepted")),
	route("POST", "/api/candidacies/{id}/interview/stages/{stage}/mock-turns", sends("InterviewMockTurnRequest", "RunAccepted")),
	route("POST", "/api/candidacies/{id}/interview/stages/{stage}/feedback", sends("InterviewFeedbackRequest", "InterviewFeedback")),
}

// secretRoutes are the dashboard's management of the vault: admin routes on
// secrets, never remote. None returns a value; the one route that returns a
// password is the extension's sign-in resolve.
var secretRoutes = []Route{
	route("GET", "/api/secrets", func(r *Route) { reads("SecretList")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("PUT", "/api/secrets/{name}", func(r *Route) { sends("SecretWrite", "SecretStatus")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("DELETE", "/api/secrets/{name}", func(r *Route) { reads("SecretStatus")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("POST", "/api/secrets/{name}/test", func(r *Route) { reads("SecretTest")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("POST", "/api/secrets/alternates", func(r *Route) { sends("AlternateWrite", "AlternateAdded")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("PUT", "/api/secrets/alternates/order", func(r *Route) { sends("AlternateOrder", "SecretList")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("DELETE", "/api/secrets/alternates/{id}", func(r *Route) { reads("SecretList")(r); r.Access, r.Secret = AccessAdmin, true }),
}

// extensionRoutes serve the browser extension: the applicant snapshot for a
// fill, the run an extension fill is attached to, the posting of a form-only
// page read from its public board, the embedded build's stamp
// and the sign-in sync into the apply window. The sync routes accept cookie
// values, so they are secret and take the large body class.
var extensionRoutes = []Route{
	route("GET", "/api/autofill/applicant", reads("ApplicantSnapshot")),
	route("POST", "/api/autofill/runs", sends("FillRunRequest", "RunAccepted")),
	route("POST", "/api/autofill/runs/{id}/end", sends("FillRunEnd", "RunView")),
	route("GET", "/api/autofill/posting", reads("PostingRead")),
	route("GET", "/api/extension/build", reads("ExtensionBuild")),
	route("GET", "/api/extension/clients", reads("ExtensionClients")),
	route("DELETE", "/api/extension/clients", func(r *Route) { reads("ExtensionClients")(r); r.Access, r.SessionOnly = AccessAdmin, true }),
	route("GET", "/api/resumes", reads("ResumeList")),
	route("GET", "/api/resumes/{id}/file", reads(PayloadAsset)),
	route("POST", "/api/resumes/rank", sends("ResumeRankRequest", "ResumeRank")),
	route("POST", "/api/submissions", sends("SubmissionRecord", "SubmissionRecorded")),
	route("POST", "/api/submissions/claims", sends("PressClaimRequest", "PressClaimGranted")),
	route("POST", "/api/signin/resolve", func(r *Route) {
		sends("SigninResolveRequest", "SigninCredential")(r)
		r.Access, r.Secret = AccessAdmin, true
	}),
	route("GET", "/api/browser/sessions", reads("SessionsView")),
	route("POST", "/api/browser/sessions/sync-requests", func(r *Route) {
		sends("SessionSyncRequest", "SessionPending")(r)
		r.Access = AccessAdmin
	}),
	route("POST", "/api/browser/sessions/undo", func(r *Route) { reads("SessionUndoResult")(r); r.Access = AccessAdmin }),
	// Direct import of the user's own sign-in cookies from an installed browser.
	// Listing sources and sites reads host names only; importing reads cookie
	// values under the user's keychain approval, so it is admin and secret.
	route("GET", "/api/browser/import/sources", reads("ImportSources")),
	route("GET", "/api/browser/import/sources/{id}/sites", reads("ImportSites")),
	route("POST", "/api/browser/import", func(r *Route) {
		sends("ImportRequest", "SessionImportResult")(r)
		r.Access, r.Secret = AccessAdmin, true
	}),
	route("GET", "/api/browser/imported", reads("ImportedView")),
	route("POST", "/api/browser/imported/forget", func(r *Route) { sends("ForgetRequest", "ForgetResult")(r); r.Access = AccessAdmin }),
	route("POST", "/api/browser/sessions/check", reads("SignInChecks")),
	route("POST", "/api/browser/apply-window/open", sends("BrowserOpenRequest", "BrowserOpened")),
	route("GET", "/api/browser/status", reads("BrowserStatus")),
	route("GET", "/api/browser/lanes/{lane}/tabs/{tabId}/screenshot", remoteReads(PayloadAsset)),
	route("POST", "/api/browser/apply-window/show", sends("BrowserShowRequest", "BrowserShown")),
	// The browser panel's live view of an apply tab and the user's input and
	// navigation in it: this computer's dashboard session only.
	route("POST", "/api/browser/lanes/apply/tabs", func(r *Route) { sends("BrowserNewTabRequest", "BrowserNewTab")(r); r.SessionOnly = true }),
	route("GET", "/api/browser/lanes/apply/tabs/{tabId}/live", func(r *Route) { reads(PayloadEvents)(r); r.SessionOnly = true }),
	route("POST", "/api/browser/lanes/apply/tabs/{tabId}/input", func(r *Route) {
		sends("BrowserInputRequest", "BrowserTabSent")(r)
		r.SessionOnly, r.Limit = true, LimitInput
	}),
	route("POST", "/api/browser/lanes/apply/tabs/{tabId}/navigate", func(r *Route) {
		sends("BrowserNavigateRequest", "BrowserTabSent")(r)
		r.SessionOnly = true
	}),
	route("POST", "/api/browser/stop", func(r *Route) { sends("BrowserStopRequest", "BrowserStopped")(r); r.Access = AccessAdmin }),
	route("POST", "/api/browser/sessions/checkin", sends("SessionCheckin", "SessionCheckinReply")),
	route("POST", "/api/browser/sessions/imports", func(r *Route) { sendsLarge("SessionImport", "SessionImportResult")(r); r.Secret = true }),
	route("POST", "/api/browser/sessions/pushes", func(r *Route) { sendsLarge("SessionPush", "SessionImportResult")(r); r.Secret = true }),
}

// policyRoutes serve the detection view: signals, baselines and cooldowns.
var policyRoutes = []Route{
	route("GET", "/api/policy/risk", reads("RiskView")),
}

// gmailRoutes serve the Gmail integration: the offline health read model, the
// sync runs, and the bounded lookups the extension and the dashboard make at
// request time. A lookup returns a secret (a code or a link), so it is
// flagged secret and denied on remote listeners like every route here.
var gmailRoutes = []Route{
	route("GET", "/api/gmail/health", reads("GmailHealth")),
	route("GET", "/api/gmail/runs", reads("GmailRunList")),
	route("GET", "/api/gmail/runs/{id}", reads("GmailRun")),
	route("GET", "/api/gmail/otp/latest", func(r *Route) { reads("OneTimeCode")(r); r.Secret = true }),
	route("GET", "/api/gmail/activation-link/latest", func(r *Route) { reads("GmailAccountLink")(r); r.Secret = true }),
	route("GET", "/api/gmail/reset-link/latest", func(r *Route) { reads("GmailAccountLink")(r); r.Secret = true }),
	route("GET", "/api/gmail/signin-link/latest", func(r *Route) { reads("GmailSigninLink")(r); r.Secret = true }),
	route("GET", "/api/gmail/inbox", reads("GmailInboxPage")),
	route("GET", "/api/gmail/inbox/{threadId}", reads("GmailThreadDetail")),
	route("POST", "/api/gmail/inbox/{threadId}/done", reads("GmailThreadDone")),
	route("POST", "/api/gmail/inbox/{threadId}/reopen", reads("GmailThreadDone")),
	route("POST", "/api/gmail/inbox/{threadId}/dismiss", reads("GmailThreadDismissed")),
	route("GET", "/api/gmail/threads/{id}/summary", func(r *Route) { reads("GmailSummary")(r); r.Limit, r.Deadline = LimitEvaluation, modelDeadline }),
	route("GET", "/api/gmail/threads/{id}/draft", reads("GmailThreadDraft")),
	route("POST", "/api/gmail/threads/{id}/draft", func(r *Route) {
		reads("GmailThreadDraft")(r)
		r.SessionOnly, r.Limit, r.Deadline = true, LimitEvaluation, modelDeadline
	}),
	route("GET", "/api/gmail/links", reads("GmailLinkList")),
	route("POST", "/api/gmail/links/{signalId}/confirm", sends("GmailLinkConfirm", "GmailLinkConfirmed")),
	route("POST", "/api/gmail/links/{signalId}/reject", reads("GmailLinkRejected")),
	route("POST", "/api/gmail/links/{signalId}/restore", reads("GmailLinkRestored")),
	route("GET", "/api/gmail/confirmations", reads("GmailConfirmationList")),
	route("GET", "/api/gmail/moves", reads("GmailMoveList")),
	route("POST", "/api/gmail/confirmations/{id}/confirm", reads("GmailConfirmationDecided")),
	route("POST", "/api/gmail/confirmations/{id}/dismiss", reads("GmailConfirmationDecided")),
	route("POST", "/api/gmail/confirmations/{id}/restore", reads("GmailConfirmationDecided")),
	route("GET", "/api/gmail/digests", reads("GmailDigestList")),
	route("GET", "/api/gmail/digests/feedback", reads("GmailDigestVerdicts")),
	route("GET", "/api/gmail/digests/{id}", reads("GmailDigest")),
	route("POST", "/api/gmail/digests/{id}/feedback", sends("GmailDigestFeedback", "GmailDigestRated")),
	route("POST", "/api/gmail/digests/run", reads("RunAccepted")),
	route("GET", "/api/gmail/settings", reads("GmailSettings")),
	route("PATCH", "/api/gmail/settings", sends("GmailSettingsPatch", "GmailSettings")),
	route("POST", "/api/gmail/sync", reads("GmailSyncStarted")),
	route("PUT", "/api/gmail/account", func(r *Route) {
		sends("GmailConnect", "GmailConnected")(r)
		r.Access, r.Secret, r.Deadline = AccessAdmin, true, gmailSignInDeadline
	}),
	route("POST", "/api/gmail/account/test", func(r *Route) {
		reads("GmailTest")(r)
		r.Access, r.Deadline = AccessAdmin, gmailSignInDeadline
	}),
	route("DELETE", "/api/gmail/account", func(r *Route) { reads("GmailHealth")(r); r.Access, r.Secret = AccessAdmin, true }),
}

// scheduleRoutes serve the user's schedules, their history, the job kinds and
// the preset packs. Listing packs writes nothing; applying one creates ordinary
// schedules marked with the pack.
var scheduleRoutes = []Route{
	route("GET", "/api/schedules", remoteReads("ScheduleList")),
	route("POST", "/api/schedules", sends("ScheduleCreate", "Schedule")),
	route("GET", "/api/schedules/{id}", reads("Schedule")),
	route("PATCH", "/api/schedules/{id}", sends("SchedulePatch", "Schedule")),
	route("DELETE", "/api/schedules/{id}", reads("ScheduleList")),
	route("POST", "/api/schedules/{id}/run", reads("RunAccepted")),
	route("GET", "/api/schedules/{id}/history", reads("ScheduleHistory")),
	route("POST", "/api/schedules/preview", sends("SchedulePreviewRequest", "SchedulePreview")),
	route("POST", "/api/schedules/pause-all", reads("SchedulePauseResult")),
	route("POST", "/api/schedules/resume-all", reads("SchedulePauseResult")),
	route("GET", "/api/schedule-kinds", reads("ScheduleKindList")),
	route("GET", "/api/schedule-packs", reads("SchedulePackList")),
	route("POST", "/api/schedule-packs/{id}/apply", reads("SchedulePackResult")),
	route("DELETE", "/api/schedule-packs/{id}", reads("SchedulePackDeleted")),
}

// chatRoutes serve the Ask Agent's conversations. Reading is standard access;
// every route that steers, cancels, forks, renames or deletes accepts the
// dashboard session only. Sending a message takes the dashboard session, whose
// turn has every tool class, or a remote device, whose turn has the read and
// draft classes only; the handler refuses the local token, which this route
// never accepted.
var chatRoutes = []Route{
	route("GET", "/api/chat/sessions", reads("ChatSessionPage")),
	route("GET", "/api/chat/sessions/{id}", reads("ChatSessionDetail")),
	route("PATCH", "/api/chat/sessions/{id}", func(r *Route) { sends("ChatRename", "ChatSession")(r); r.SessionOnly = true }),
	route("DELETE", "/api/chat/sessions/{id}", func(r *Route) { reads("ChatDeleted")(r); r.SessionOnly = true }),
	route("POST", "/api/chat/sessions/{id}/messages", func(r *Route) { sends("ChatSend", "ChatTurnAccepted")(r); r.Remote = RemoteAllowed }),
	route("GET", "/api/chat/sessions/{id}/events", remoteReads(PayloadEvents)),
	route("POST", "/api/chat/sessions/{id}/steer", func(r *Route) { sends("ChatSteer", "ChatSteerAccepted")(r); r.SessionOnly = true }),
	route("POST", "/api/chat/sessions/{id}/cancel", func(r *Route) { reads("ChatCancelResult")(r); r.SessionOnly = true }),
	route("POST", "/api/chat/sessions/{id}/fork", func(r *Route) { sends("ChatFork", "ChatSession")(r); r.SessionOnly = true }),
}

// agentRoutes are the bridge the extension inside pi calls, on the loopback
// listener under the process's own credential.
var agentRoutes = []Route{
	route("GET", "/internal/agent/catalog", func(r *Route) { reads("AgentCatalog")(r); r.Access = AccessAgentProcess }),
	route("POST", "/internal/agent/hello", func(r *Route) { sends("AgentHello", "AgentHelloResult")(r); r.Access = AccessAgentProcess }),
	route("GET", "/internal/agent/turn-context", func(r *Route) { reads("AgentTurnContext")(r); r.Access = AccessAgentProcess }),
	// A forwarded call is answered inside the longest tool timeout.
	route("POST", "/internal/agent/tool-call", func(r *Route) {
		sends("AgentToolCall", "AgentToolResult")(r)
		r.Access, r.Deadline = AccessAgentProcess, agent.MaxToolTimeout+10*time.Second
	}),
	// A model call of the turn, which the gateway relays as the provider streams it.
	route("POST", agent.ModelPathOpenAI, modelCall),
	route("POST", agent.ModelPathAnthropic, modelCall),
	route("POST", agent.ModelPathResponses, modelCall),
}

func modelCall(r *Route) {
	r.Body, r.Request, r.Response, r.Access = BodyModel, PayloadModel, PayloadModel, AccessAgentProcess
}

// memoryRoutes serve the Memory tab and the Ask Agent switches. Reading is
// local; every change is the user's act in the dashboard session, because what
// is stored here reaches the model's prompt: approving a rule or a fact is the
// one way it gets there, and no tool and no remote device has a path to it.
var memoryRoutes = []Route{
	route("GET", "/api/agent/memory/facts", reads("MemoryFactList")),
	route("PATCH", "/api/agent/memory/facts/{id}", func(r *Route) { sends("MemoryFactPatch", "MemoryFactEdited")(r); r.SessionOnly = true }),
	route("DELETE", "/api/agent/memory/facts/{id}", func(r *Route) { reads("MemoryDeleted")(r); r.SessionOnly = true }),
	route("POST", "/api/agent/memory/facts/{id}/approve", func(r *Route) { reads("MemoryFact")(r); r.SessionOnly = true }),
	route("GET", "/api/agent/memory/rules", reads("MemoryRuleList")),
	route("PATCH", "/api/agent/memory/rules/{id}", func(r *Route) { sends("MemoryRulePatch", "MemoryRule")(r); r.SessionOnly = true }),
	route("DELETE", "/api/agent/memory/rules/{id}", func(r *Route) { reads("MemoryDeleted")(r); r.SessionOnly = true }),
	route("POST", "/api/agent/memory/rules/{id}/approve", func(r *Route) { reads("MemoryRule")(r); r.SessionOnly = true }),
	route("POST", "/api/agent/memory/rules/{id}/retire", func(r *Route) { reads("MemoryRule")(r); r.SessionOnly = true }),
	route("GET", "/api/agent/memory/review", reads("MemoryReviewList")),
	route("DELETE", "/api/agent/memory/review/{id}", func(r *Route) { reads("MemoryDeleted")(r); r.SessionOnly = true }),
	route("GET", "/api/agent/memory/export", reads("MemoryExport")),
	route("GET", "/api/agent/settings", reads("AgentSettings")),
	route("PATCH", "/api/agent/settings", sends("AgentSettingsPatch", "AgentSettings")),
	route("GET", "/api/agent/harness", reads("AgentHarness")),
	route("POST", "/api/agent/harness/restore", func(r *Route) { sends("AgentHarnessRestore", "AgentHarness")(r); r.SessionOnly = true }),
	route("POST", "/api/agent/harness/round", func(r *Route) { reads("AgentHarness")(r); r.SessionOnly = true }),
}

// notifyRoutes serve the runs board, the needs-you inbox, the notification
// stream and settings, and the setup of the Telegram gateway. Storing a bot
// token, pairing and unpairing are admin actions on a secret.
var notifyRoutes = []Route{
	route("GET", "/api/runs", remoteReads("BoardEntryList")),
	route("GET", "/api/runs/summary", remoteReads("BoardSummary")),
	route("POST", "/api/runs/{id}/seen", remoteReads("BoardEntry")),
	route("POST", "/api/runs/{id}/ack", remoteReads("BoardEntry")),
	route("POST", "/api/runs/{id}/done", reads("BoardEntry")),
	route("GET", "/api/needs-you", remoteReads("NeedsYouInbox")),
	route("GET", "/api/needs-you/counts", reads("NeedsYouCounts")),
	route("GET", "/api/day", reads("DayView")),
	route("POST", "/api/notifications/{announcementId}/ack", remoteReads("BoardEntry")),
	route("GET", "/api/notifications/events", remoteReads(PayloadEvents)),
	route("GET", "/api/notifications/settings", reads("NotificationSettings")),
	route("PATCH", "/api/notifications/settings", sends("NotificationSettingsPatch", "NotificationSettings")),
	route("POST", "/api/notifications/test", reads("NotificationTest")),
	route("GET", "/api/notices", reads("NoticeList")),
	route("POST", "/api/notices/read", sends("NoticesRead", "NoticeList")),
	route("GET", "/api/telegram/status", reads("TelegramStatus")),
	route("PUT", "/api/telegram/token", func(r *Route) {
		sends("TelegramTokenRequest", "TelegramStatus")(r)
		r.Access, r.Secret = AccessAdmin, true
	}),
	route("DELETE", "/api/telegram/token", func(r *Route) { reads("TelegramStatus")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("POST", "/api/telegram/pairing-code", func(r *Route) { reads("TelegramPairingCode")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("DELETE", "/api/telegram/pairing", func(r *Route) { reads("TelegramStatus")(r); r.Access = AccessAdmin }),
}

// setupRoutes serve the setup wizard: the step catalog with its statuses, the
// step writers, the decisions and the doctor. The rendering diagnosis is the
// resumes step's separate subcheck.
var setupRoutes = []Route{
	route("POST", "/api/setup/cv/extract", func(r *Route) { r.Body, r.Response = BodyUpload, "CVExtracted" }),
	route("GET", "/api/setup/state", reads("SetupState")),
	route("GET", "/api/setup/status", reads("SetupStatus")),
	route("POST", "/api/setup/demo", func(r *Route) { reads("DemoCreated")(r); r.SessionOnly = true }),
	route("GET", "/demo/sample-application", func(r *Route) { reads(PayloadAsset)(r); r.Access = AccessPublic }),
	route("POST", "/api/setup/steps/{id}/apply", sends("SetupApplyRequest", "SetupApplied")),
	route("POST", "/api/setup/steps/{id}/ack", reads("SetupStep")),
	route("POST", "/api/setup/steps/{id}/skip", reads("SetupStep")),
	route("POST", "/api/setup/steps/{id}/unskip", reads("SetupStep")),
	route("POST", "/api/setup/doctor", reads("DoctorReport")),
	route("POST", "/api/setup/intake/preview", sendsLarge("IntakeRequest", "IntakePreview")),
	route("POST", "/api/setup/intake/accept", sendsLarge("IntakeAcceptRequest", "IntakeResult")),
	route("GET", "/api/documents/toolchain", reads("DocumentToolchain")),
	route("POST", "/api/documents/toolchain/smoke", reads("DocumentToolchain")),
}

// routeDefs is the route table. The static catch-all is last: it matches every
// GET path no other route claims.
var routeDefs = slices.Concat(coreRoutes, trackerRoutes, evaluationRoutes, discoveryRoutes, signalRoutes, llmRoutes, secretRoutes, systemRoutes, cliInstallRoutes, knowledgeRoutes, configRoutes, answerBankRoutes, answerRoutes, documentRoutes, extensionRoutes, policyRoutes, gmailRoutes, scheduleRoutes, chatRoutes, remoteRoutes, confirmationRoutes, goalRoutes, agentTaskRoutes, scheduleProposalRoutes, memoryRoutes, promptRoutes, computerUseRoutes, agentRoutes, notifyRoutes, setupRoutes, []Route{
	{Method: "GET", Path: "/{path...}", Access: AccessPublic, Remote: RemoteAllowed, Limit: LimitAsset, Body: BodyNone,
		Request: PayloadNone, Response: PayloadAsset},
})

// Routes returns a copy of the route table, for `applysmith routes`.
func Routes() []Route { return slices.Clone(routeDefs) }

// FormatRoutes renders the table, one route per line.
func FormatRoutes(routes []Route) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-38s %-9s %-8s %-11s %-7s %s\n", "METHOD", "PATH", "ACCESS", "REMOTE", "LIMIT", "BODY", "FLAGS")
	for _, r := range routes {
		var flags []string
		if r.Secret {
			flags = append(flags, "secret")
		}
		if r.SessionOnly {
			flags = append(flags, "session-only")
		}
		fmt.Fprintf(&b, "%-6s %-38s %-9s %-8s %-11s %-7s %s\n", r.Method, r.Path, r.Access, r.Remote, r.Limit, r.Body, strings.Join(flags, ","))
	}
	return b.String()
}

var (
	methods     = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	accessSet   = []Access{AccessPublic, AccessStandard, AccessAdmin, AccessAgentProcess}
	remoteSet   = []Remote{RemoteAllowed, RemoteDenied}
	limitSet    = []Limit{LimitGeneral, LimitEvaluation, LimitPairing, LimitInput, LimitAsset}
	bodySet     = []Body{BodyNone, BodyJSON, BodyJSONLarge, BodyUpload, BodyModel}
	payloadSpec = []string{PayloadNone, PayloadEvents, PayloadAsset, PayloadModel}
)

// validateRoutes returns one problem per violated table rule: a missing or
// unknown field, a forbidden combination, a duplicate, or a wire type that the
// wire package does not define.
func validateRoutes(routes []Route) []string {
	var problems []string
	seen := map[string]bool{}
	for _, r := range routes {
		name := r.Pattern()
		bad := func(format string, args ...any) { problems = append(problems, name+": "+fmt.Sprintf(format, args...)) }
		switch {
		case !slices.Contains(methods, r.Method):
			bad("unknown method %q", r.Method)
		case !strings.HasPrefix(r.Path, "/"):
			bad("path must start with a slash")
		}
		if seen[name] {
			bad("declared twice")
		}
		seen[name] = true
		if !slices.Contains(accessSet, r.Access) {
			bad("missing or unknown access class %q", r.Access)
		}
		if !slices.Contains(remoteSet, r.Remote) {
			bad("missing or unknown remote flag %q", r.Remote)
		}
		if r.Limit != limitHealth && !slices.Contains(limitSet, r.Limit) {
			bad("missing or unknown limit class %q", r.Limit)
		}
		if r.Limit == LimitAsset && (r.Access != AccessPublic || r.Response != PayloadAsset) {
			bad("the asset limit class is for public file routes only")
		}
		if !slices.Contains(bodySet, r.Body) {
			bad("missing or unknown body class %q", r.Body)
		}
		for _, p := range []struct{ field, value string }{{"request", r.Request}, {"response", r.Response}} {
			if _, ok := wire.Types[p.value]; !ok && !slices.Contains(payloadSpec, p.value) {
				bad("%s type %q is neither a wire type nor one of %v", p.field, p.value, payloadSpec)
			}
		}
		if r.Access == AccessAgentProcess && (r.SessionOnly || !strings.HasPrefix(r.Path, "/internal/agent/")) {
			bad("a process route lives under /internal/agent/ and takes no session")
		}
		if r.Access != AccessAgentProcess && strings.HasPrefix(r.Path, "/internal/agent/") {
			bad("a route under /internal/agent/ must be a process route")
		}
		if (r.Access == AccessAdmin || r.Secret || r.SessionOnly || r.Access == AccessAgentProcess) && r.Remote == RemoteAllowed {
			bad("admin, secret and session-only routes cannot be remote: allowed")
		}
		if r.SessionOnly && r.Access == AccessPublic {
			bad("a public route has no session to require")
		}
		if (r.Method == "GET" || r.Method == "HEAD") && r.Body != BodyNone {
			bad("a read route takes no body")
		}
		if r.Body == BodyNone && r.Request != PayloadNone {
			bad("a route without a body names request type %q", r.Request)
		}
		if (r.Body == BodyJSON || r.Body == BodyJSONLarge) && r.Request == PayloadNone {
			bad("a json route must name its request type")
		}
		if (r.Body == BodyModel || r.Request == PayloadModel || r.Response == PayloadModel) &&
			(r.Body != BodyModel || r.Request != PayloadModel || r.Response != PayloadModel || r.Access != AccessAgentProcess) {
			bad("a model call is a process route with a model body and a model reply")
		}
	}
	return problems
}

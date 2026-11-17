package wire

import "reflect"

// Success is the envelope of a successful call.
type Success[T any] struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"requestId"`
	Data      T      `json:"data"`
}

// Failure is the envelope of a failed call.
type Failure struct {
	OK        bool      `json:"ok"`
	RequestID string    `json:"requestId"`
	Error     ErrorBody `json:"error"`
}

// ErrorBody describes one failure. Details is code-specific and may be absent.
type ErrorBody struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Details   any       `json:"details,omitempty"`
	Retryable bool      `json:"retryable"`
}

// Reasons carried in ErrorBody.Details.reason.
const (
	ReasonHostNotAllowed = "host_not_allowed"
	ReasonOriginMismatch = "origin_mismatch"
	ReasonCrossSite      = "cross_site"
	ReasonTooManyStreams = "too_many_streams"
	ReasonSessionOnly    = "session_only"
	ReasonDemoWorkspace  = "demo_workspace"
	// ReasonNoModelRoute is a model task with no model routed to it. It is also
	// the reason of an Ask Agent turn that ended for want of a model the agent
	// can call (ChatEvent.Reason and ChatTurn.Reason).
	ReasonNoModelRoute = "no_model_route"
)

// ErrorDetails is the common shape of ErrorBody.Details.
type ErrorDetails struct {
	Reason            string `json:"reason,omitempty"`
	Field             string `json:"field,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
	// MatchID names the row a duplicate conflict matched.
	MatchID SafeInt `json:"matchId,omitempty"`
	// RunID names the run a conflict waits on, such as the document run
	// already working for the application.
	RunID string `json:"runId,omitempty"`
	// Fields maps an input field to what is wrong with it, for a call that
	// rejects several fields at once.
	Fields map[string]string `json:"fields,omitempty"`
}

// HealthStatus is the overall state reported by the health route.
type HealthStatus string

const (
	HealthStarting HealthStatus = "starting"
	HealthOK       HealthStatus = "ok"
	HealthDegraded HealthStatus = "degraded"
)

// ComponentStatus is the state of one health component.
type ComponentStatus string

const (
	ComponentOK           ComponentStatus = "ok"
	ComponentDegraded     ComponentStatus = "degraded"
	ComponentDown         ComponentStatus = "down"
	ComponentUnconfigured ComponentStatus = "unconfigured"
)

// Component is one entry of the detailed health report.
type Component struct {
	Name   string          `json:"name"`
	Status ComponentStatus `json:"status"`
	Detail string          `json:"detail"`
}

// Health is the body of GET /api/health. Probe diagnostics appear only
// for an authenticated caller.
type Health struct {
	Status              HealthStatus `json:"status"`
	APIVersion          int          `json:"apiVersion"`
	Version             string       `json:"version"`
	BuildID             string       `json:"buildId"`
	PathKey             string       `json:"workspaceId"`
	StartedAt           Time         `json:"startedAt"`
	UptimeSeconds       *SafeInt     `json:"uptimeSeconds,omitempty"`
	SchemaVersion       *SafeInt     `json:"schemaVersion,omitempty"`
	Components          []Component  `json:"components,omitempty"`
	ComponentsCheckedAt *Time        `json:"componentsCheckedAt,omitempty"`
	ComponentsProbeMS   *SafeInt     `json:"componentsProbeMs,omitempty"`
}

// PairingCode is a one-time code for the extension to redeem.
type PairingCode struct {
	Code      string `json:"code"`
	ExpiresAt Time   `json:"expiresAt"`
}

// LaunchCode is a fresh one-time code that opens the dashboard once.
type LaunchCode struct {
	Code      string `json:"code"`
	ExpiresAt Time   `json:"expiresAt"`
}

// PairingClaim redeems a pairing code.
type PairingClaim struct {
	Code string `json:"code"`
}

// TokenIssued carries a bearer token, only in the response that issues it.
type TokenIssued struct {
	Token string `json:"token"`
}

// BackupRequest asks the owning server for a consistent backup. Out must not
// exist yet and must lie outside the workspace.
type BackupRequest struct {
	Out string `json:"out"`
}

// BackupResult describes the backup that was written.
type BackupResult struct {
	Out           string  `json:"out"`
	SchemaVersion SafeInt `json:"schemaVersion"`
	WorkspaceID   string  `json:"workspaceId"`
	FileCount     SafeInt `json:"fileCount"`
	CreatedAt     string  `json:"createdAt"`
}

// States of a live backup.
const (
	BackupRunning   = "running"
	BackupSucceeded = "succeeded"
	BackupFailed    = "failed"
)

// BackupOperation is the state of one live backup: Result is set once it
// succeeded, Error once it failed.
type BackupOperation struct {
	ID     string        `json:"id"`
	State  string        `json:"state"`
	Result *BackupResult `json:"result,omitempty"`
	Error  *ErrorBody    `json:"error,omitempty"`
}

// Topics is the closed set of board topics.
var Topics = []string{
	"runs", "tracker", "inbox", "scans", "schedules", "browser",
	"gmail", "setup", "notifications", "reports", "answerbank", "configuration",
}

// BoardSnapshot is the first event of the board stream.
type BoardSnapshot struct {
	Revisions map[string]SafeInt `json:"revisions"`
}

// BoardNotice announces a new revision of one topic. File names the changed
// workspace file for the configuration topic.
type BoardNotice struct {
	Topic    string  `json:"topic"`
	Revision SafeInt `json:"revision"`
	File     string  `json:"file,omitempty"`
}

// Reset reasons.
const (
	ResetCursorExpired = "cursor_expired"
	ResetServerRestart = "server_restarted"
	ResetSlowConsumer  = "slow_consumer"
)

// StreamReset tells a client to refetch a snapshot.
type StreamReset struct {
	Reason string `json:"reason"`
}

// StreamShutdown is sent before the server closes a stream.
type StreamShutdown struct{}

// Types names every wire struct the route table and the generator may refer to.
var Types = map[string]reflect.Type{
	"SetupReason":            reflect.TypeFor[SetupReason](),
	"SetupField":             reflect.TypeFor[SetupField](),
	"SetupStep":              reflect.TypeFor[SetupStep](),
	"SetupSecret":            reflect.TypeFor[SetupSecret](),
	"SetupState":             reflect.TypeFor[SetupState](),
	"SetupStatus":            reflect.TypeFor[SetupStatus](),
	"DemoCreated":            reflect.TypeFor[DemoCreated](),
	"SetupApplyRequest":      reflect.TypeFor[SetupApplyRequest](),
	"SetupDraft":             reflect.TypeFor[SetupDraft](),
	"CVExtracted":            reflect.TypeFor[CVExtracted](),
	"SetupApplied":           reflect.TypeFor[SetupApplied](),
	"IntakeDocument":         reflect.TypeFor[IntakeDocument](),
	"IntakeRequest":          reflect.TypeFor[IntakeRequest](),
	"IntakeSource":           reflect.TypeFor[IntakeSource](),
	"IntakeOption":           reflect.TypeFor[IntakeOption](),
	"IntakeField":            reflect.TypeFor[IntakeField](),
	"IntakeItem":             reflect.TypeFor[IntakeItem](),
	"IntakeDocInfo":          reflect.TypeFor[IntakeDocInfo](),
	"IntakePreview":          reflect.TypeFor[IntakePreview](),
	"IntakeSelection":        reflect.TypeFor[IntakeSelection](),
	"IntakeAcceptRequest":    reflect.TypeFor[IntakeAcceptRequest](),
	"IntakeResult":           reflect.TypeFor[IntakeResult](),
	"DoctorResult":           reflect.TypeFor[DoctorResult](),
	"DoctorReport":           reflect.TypeFor[DoctorReport](),
	"ToolchainComponent":     reflect.TypeFor[ToolchainComponent](),
	"ToolchainExternal":      reflect.TypeFor[ToolchainExternal](),
	"ToolchainBudget":        reflect.TypeFor[ToolchainBudget](),
	"DocumentToolchain":      reflect.TypeFor[DocumentToolchain](),
	"ErrorBody":              reflect.TypeFor[ErrorBody](),
	"ErrorDetails":           reflect.TypeFor[ErrorDetails](),
	"Component":              reflect.TypeFor[Component](),
	"Health":                 reflect.TypeFor[Health](),
	"PairingCode":            reflect.TypeFor[PairingCode](),
	"LaunchCode":             reflect.TypeFor[LaunchCode](),
	"RemoteStatus":           reflect.TypeFor[RemoteStatus](),
	"RemoteListener":         reflect.TypeFor[RemoteListener](),
	"RemoteTunnel":           reflect.TypeFor[RemoteTunnel](),
	"RemoteOffer":            reflect.TypeFor[RemoteOffer](),
	"RemoteQR":               reflect.TypeFor[RemoteQR](),
	"RemotePairClaim":        reflect.TypeFor[RemotePairClaim](),
	"RemoteClaimed":          reflect.TypeFor[RemoteClaimed](),
	"RemoteDevice":           reflect.TypeFor[RemoteDevice](),
	"RemoteDeviceList":       reflect.TypeFor[RemoteDeviceList](),
	"RemoteRevoked":          reflect.TypeFor[RemoteRevoked](),
	"RemoteMe":               reflect.TypeFor[RemoteMe](),
	"RemoteEvent":            reflect.TypeFor[RemoteEvent](),
	"RemoteEventList":        reflect.TypeFor[RemoteEventList](),
	"PairingClaim":           reflect.TypeFor[PairingClaim](),
	"TokenIssued":            reflect.TypeFor[TokenIssued](),
	"BackupRequest":          reflect.TypeFor[BackupRequest](),
	"BackupResult":           reflect.TypeFor[BackupResult](),
	"BackupOperation":        reflect.TypeFor[BackupOperation](),
	"BoardSnapshot":          reflect.TypeFor[BoardSnapshot](),
	"BoardNotice":            reflect.TypeFor[BoardNotice](),
	"StreamReset":            reflect.TypeFor[StreamReset](),
	"StreamShutdown":         reflect.TypeFor[StreamShutdown](),
	"Candidacy":              reflect.TypeFor[Candidacy](),
	"CandidacyPage":          reflect.TypeFor[CandidacyPage](),
	"CandidacyEvent":         reflect.TypeFor[CandidacyEvent](),
	"CandidacyDetail":        reflect.TypeFor[CandidacyDetail](),
	"StatusChangeRequest":    reflect.TypeFor[StatusChangeRequest](),
	"StatusChangeResult":     reflect.TypeFor[StatusChangeResult](),
	"NoteRequest":            reflect.TypeFor[NoteRequest](),
	"CandidacyCreate":        reflect.TypeFor[CandidacyCreate](),
	"BulkStatusRequest":      reflect.TypeFor[BulkStatusRequest](),
	"BulkStatusResult":       reflect.TypeFor[BulkStatusResult](),
	"TrackerSummary":         reflect.TypeFor[TrackerSummary](),
	"TrackerDuplicates":      reflect.TypeFor[TrackerDuplicates](),
	"DuplicateMergeRequest":  reflect.TypeFor[DuplicateMergeRequest](),
	"DuplicateMergeResult":   reflect.TypeFor[DuplicateMergeResult](),
	"DuplicateDismissal":     reflect.TypeFor[DuplicateDismissal](),
	"BulkArchiveRequest":     reflect.TypeFor[BulkArchiveRequest](),
	"BulkArchiveResult":      reflect.TypeFor[BulkArchiveResult](),
	"FollowupList":           reflect.TypeFor[FollowupList](),
	"FollowupRequest":        reflect.TypeFor[FollowupRequest](),
	"FollowupDraftRequest":   reflect.TypeFor[FollowupDraftRequest](),
	"OutreachDraftRequest":   reflect.TypeFor[OutreachDraftRequest](),
	"MessageDraft":           reflect.TypeFor[MessageDraft](),
	"OutcomeStageReach":      reflect.TypeFor[OutcomeStageReach](),
	"OutcomeGroup":           reflect.TypeFor[OutcomeGroup](),
	"TrackerOutcomes":        reflect.TypeFor[TrackerOutcomes](),
	"GapRequest":             reflect.TypeFor[GapRequest](),
	"GapConfirmRequest":      reflect.TypeFor[GapConfirmRequest](),
	"GapPlanRequest":         reflect.TypeFor[GapPlanRequest](),
	"GapProgressRequest":     reflect.TypeFor[GapProgressRequest](),
	"GapSource":              reflect.TypeFor[GapSource](),
	"Gap":                    reflect.TypeFor[Gap](),
	"GapInferred":            reflect.TypeFor[GapInferred](),
	"GapExclusion":           reflect.TypeFor[GapExclusion](),
	"GapInput":               reflect.TypeFor[GapInput](),
	"GapResource":            reflect.TypeFor[GapResource](),
	"GapPlanItem":            reflect.TypeFor[GapPlanItem](),
	"GapLookup":              reflect.TypeFor[GapLookup](),
	"GapPlan":                reflect.TypeFor[GapPlan](),
	"GapReport":              reflect.TypeFor[GapReport](),
	"GapSummary":             reflect.TypeFor[GapSummary](),
	"GapList":                reflect.TypeFor[GapList](),
	"CandidacyTransitions":   reflect.TypeFor[CandidacyTransitions](),
	"StatusHistoryItem":      reflect.TypeFor[StatusHistoryItem](),
	"ApplicationLookup":      reflect.TypeFor[ApplicationLookup](),
	"ApplyItem":              reflect.TypeFor[ApplyItem](),
	"ApplyLane":              reflect.TypeFor[ApplyLane](),
	"ApplyList":              reflect.TypeFor[ApplyList](),
	"ApplySkipStatus":        reflect.TypeFor[ApplySkipStatus](),
	"AutofillControl":        reflect.TypeFor[AutofillControl](),
	"AutofillTabResult":      reflect.TypeFor[AutofillTabResult](),
	"ApplyRunRequest":        reflect.TypeFor[ApplyRunRequest](),
	"ApplyBatchRequest":      reflect.TypeFor[ApplyBatchRequest](),
	"ApplySkippedRow":        reflect.TypeFor[ApplySkippedRow](),
	"ApplyBatchAccepted":     reflect.TypeFor[ApplyBatchAccepted](),
	"ApplySkipList":          reflect.TypeFor[ApplySkipList](),
	"ApplySkipListWrite":     reflect.TypeFor[ApplySkipListWrite](),
	"ApplyRunView":           reflect.TypeFor[ApplyRunView](),
	"ApplyRetryRequest":      reflect.TypeFor[ApplyRetryRequest](),
	"ApplySubmitRequest":     reflect.TypeFor[ApplySubmitRequest](),
	"ApplyAnswerRequest":     reflect.TypeFor[ApplyAnswerRequest](),
	"ApplyAnswerSaved":       reflect.TypeFor[ApplyAnswerSaved](),
	"TakeoverStarted":        reflect.TypeFor[TakeoverStarted](),
	"ApplyTabShown":          reflect.TypeFor[ApplyTabShown](),
	"ApplyBatchView":         reflect.TypeFor[ApplyBatchView](),
	"RunAccepted":            reflect.TypeFor[RunAccepted](),
	"RunView":                reflect.TypeFor[RunView](),
	"RunPhase":               reflect.TypeFor[RunPhase](),
	"RunProgress":            reflect.TypeFor[RunProgress](),
	"RunFinal":               reflect.TypeFor[RunFinal](),
	"EvaluationRequest":      reflect.TypeFor[EvaluationRequest](),
	"EvaluationView":         reflect.TypeFor[EvaluationView](),
	"ReportSummary":          reflect.TypeFor[ReportSummary](),
	"ReportPage":             reflect.TypeFor[ReportPage](),
	"ReportDetail":           reflect.TypeFor[ReportDetail](),
	"ScanRequest":            reflect.TypeFor[ScanRequest](),
	"SignalRow":              reflect.TypeFor[SignalRow](),
	"SignalList":             reflect.TypeFor[SignalList](),
	"SignalDocument":         reflect.TypeFor[SignalDocument](),
	"SignalQuote":            reflect.TypeFor[SignalQuote](),
	"SignalAssessment":       reflect.TypeFor[SignalAssessment](),
	"SignalEvent":            reflect.TypeFor[SignalEvent](),
	"SignalFactor":           reflect.TypeFor[SignalFactor](),
	"SignalIdentity":         reflect.TypeFor[SignalIdentity](),
	"SignalPosting":          reflect.TypeFor[SignalPosting](),
	"SignalOutcome":          reflect.TypeFor[SignalOutcome](),
	"SignalDetail":           reflect.TypeFor[SignalDetail](),
	"SignalReviewItem":       reflect.TypeFor[SignalReviewItem](),
	"SignalReviewList":       reflect.TypeFor[SignalReviewList](),
	"SignalTickRequest":      reflect.TypeFor[SignalTickRequest](),
	"SignalEvaluateResult":   reflect.TypeFor[SignalEvaluateResult](),
	"SignalRescoreResult":    reflect.TypeFor[SignalRescoreResult](),
	"SignalLane":             reflect.TypeFor[SignalLane](),
	"SignalAlarm":            reflect.TypeFor[SignalAlarm](),
	"SignalTypeHit":          reflect.TypeFor[SignalTypeHit](),
	"SignalOutcomes":         reflect.TypeFor[SignalOutcomes](),
	"SignalSourceResult":     reflect.TypeFor[SignalSourceResult](),
	"SignalTickSummary":      reflect.TypeFor[SignalTickSummary](),
	"SignalStatus":           reflect.TypeFor[SignalStatus](),
	"ScanCounts":             reflect.TypeFor[ScanCounts](),
	"ScanRecord":             reflect.TypeFor[ScanRecord](),
	"ScanSource":             reflect.TypeFor[ScanSource](),
	"ScanSourceList":         reflect.TypeFor[ScanSourceList](),
	"LeadRunRequest":         reflect.TypeFor[LeadRunRequest](),
	"PortalCompany":          reflect.TypeFor[PortalCompany](),
	"PortalCompanyList":      reflect.TypeFor[PortalCompanyList](),
	"PortalRegistry":         reflect.TypeFor[PortalRegistry](),
	"PortalDetectRequest":    reflect.TypeFor[PortalDetectRequest](),
	"PortalCandidate":        reflect.TypeFor[PortalCandidate](),
	"PortalDetection":        reflect.TypeFor[PortalDetection](),
	"PortalTargetRoles":      reflect.TypeFor[PortalTargetRoles](),
	"SchedulePreviewRequest": reflect.TypeFor[SchedulePreviewRequest](),
	"SchedulePreview":        reflect.TypeFor[SchedulePreview](),
	"ScanList":               reflect.TypeFor[ScanList](),
	"InboxRow":               reflect.TypeFor[InboxRow](),
	"Lead":                   reflect.TypeFor[Lead](),
	"LeadPage":               reflect.TypeFor[LeadPage](),
	"InboxPage":              reflect.TypeFor[InboxPage](),
	"InboxPromoteRequest":    reflect.TypeFor[InboxPromoteRequest](),
	"InboxPromoteResult":     reflect.TypeFor[InboxPromoteResult](),
	"BlockedCompany":         reflect.TypeFor[BlockedCompany](),
	"BlockedCompanyRequest":  reflect.TypeFor[BlockedCompanyRequest](),
	"BlockedCompanyList":     reflect.TypeFor[BlockedCompanyList](),
	"LLMProvider":            reflect.TypeFor[LLMProvider](),
	"LLMFallback":            reflect.TypeFor[LLMFallback](),
	"LLMRoute":               reflect.TypeFor[LLMRoute](),
	"LLMTaskRoute":           reflect.TypeFor[LLMTaskRoute](),
	"LLMPreset":              reflect.TypeFor[LLMPreset](),
	"LLMRouting":             reflect.TypeFor[LLMRouting](),
	"LLMTestRequest":         reflect.TypeFor[LLMTestRequest](),
	"LLMTestStarted":         reflect.TypeFor[LLMTestStarted](),
	"SystemInfo":             reflect.TypeFor[SystemInfo](),
	"LogLines":               reflect.TypeFor[LogLines](),
	"CLIInstall":             reflect.TypeFor[CLIInstall](),
	"LLMRoutingPatch":        reflect.TypeFor[LLMRoutingPatch](),
	"LLMPresetRequest":       reflect.TypeFor[LLMPresetRequest](),
	"LLMKeyRequest":          reflect.TypeFor[LLMKeyRequest](),
	"LLMKeyStatus":           reflect.TypeFor[LLMKeyStatus](),
	"ChatGPTStatus":          reflect.TypeFor[ChatGPTStatus](),
	"ChatGPTSignInStarted":   reflect.TypeFor[ChatGPTSignInStarted](),
	"ChatGPTSignOutRequest":  reflect.TypeFor[ChatGPTSignOutRequest](),
	"ChatGPTSignOutResult":   reflect.TypeFor[ChatGPTSignOutResult](),
	"ChatGPTModels":          reflect.TypeFor[ChatGPTModels](),
	"LLMCostRow":             reflect.TypeFor[LLMCostRow](),
	"LLMCost":                reflect.TypeFor[LLMCost](),
	"UsageCall":              reflect.TypeFor[UsageCall](),
	"UsageCallPage":          reflect.TypeFor[UsageCallPage](),
	"UsageDay":               reflect.TypeFor[UsageDay](),
	"UsageDays":              reflect.TypeFor[UsageDays](),
	"UsageTrace":             reflect.TypeFor[UsageTrace](),
	"UsageTraceList":         reflect.TypeFor[UsageTraceList](),
	"UsageSpan":              reflect.TypeFor[UsageSpan](),
	"UsageTraceDetail":       reflect.TypeFor[UsageTraceDetail](),
	"KBPrincipleGrade":       reflect.TypeFor[KBPrincipleGrade](),
	"KBShapeGrade":           reflect.TypeFor[KBShapeGrade](),
	"KBCoverage":             reflect.TypeFor[KBCoverage](),
	"KBFileNote":             reflect.TypeFor[KBFileNote](),
	"KBIngestReport":         reflect.TypeFor[KBIngestReport](),
	"KBRepo":                 reflect.TypeFor[KBRepo](),
	"KBRepoAdd":              reflect.TypeFor[KBRepoAdd](),
	"KBRepoList":             reflect.TypeFor[KBRepoList](),
	"KBGitHubAccount":        reflect.TypeFor[KBGitHubAccount](),
	"KBGitHubSignIn":         reflect.TypeFor[KBGitHubSignIn](),
	"KBGitHubRepo":           reflect.TypeFor[KBGitHubRepo](),
	"KBGitHubRepos":          reflect.TypeFor[KBGitHubRepos](),
	"KBPageRef":              reflect.TypeFor[KBPageRef](),
	"KBPageLink":             reflect.TypeFor[KBPageLink](),
	"KBIngestRequest":        reflect.TypeFor[KBIngestRequest](),
	"KBMiningStart":          reflect.TypeFor[KBMiningStart](),
	"KBMiningReply":          reflect.TypeFor[KBMiningReply](),
	"KBMiningValue":          reflect.TypeFor[KBMiningValue](),
	"KBMiningEntry":          reflect.TypeFor[KBMiningEntry](),
	"KBMiningQuestion":       reflect.TypeFor[KBMiningQuestion](),
	"KBMiningSession":        reflect.TypeFor[KBMiningSession](),
	"KBMiningList":           reflect.TypeFor[KBMiningList](),
	"KBQuadrantSuggestion":   reflect.TypeFor[KBQuadrantSuggestion](),
	"KBQuadrantSuggestions":  reflect.TypeFor[KBQuadrantSuggestions](),
	"KBDraft":                reflect.TypeFor[KBDraft](),
	"KBDraftList":            reflect.TypeFor[KBDraftList](),
	"KBDraftChange":          reflect.TypeFor[KBDraftChange](),
	"KBUndo":                 reflect.TypeFor[KBUndo](),
	"KBRestored":             reflect.TypeFor[KBRestored](),

	"ProviderAccounts":          reflect.TypeFor[ProviderAccounts](),
	"ProviderAccount":           reflect.TypeFor[ProviderAccount](),
	"ProviderUsage":             reflect.TypeFor[ProviderUsage](),
	"ProviderUsageWindow":       reflect.TypeFor[ProviderUsageWindow](),
	"ProviderCredentialRequest": reflect.TypeFor[ProviderCredentialRequest](),
	"LLMProviderRequest":        reflect.TypeFor[LLMProviderRequest](),
	"LLMPriceRequest":           reflect.TypeFor[LLMPriceRequest](),

	"ConfigIssue":              reflect.TypeFor[ConfigIssue](),
	"ConfigDocument":           reflect.TypeFor[ConfigDocument](),
	"ConfigBackupList":         reflect.TypeFor[ConfigBackupList](),
	"CVDraft":                  reflect.TypeFor[CVDraft](),
	"CVDraftDismiss":           reflect.TypeFor[CVDraftDismiss](),
	"ConfigSave":               reflect.TypeFor[ConfigSave](),
	"ConfigOp":                 reflect.TypeFor[ConfigOp](),
	"ConfigPatch":              reflect.TypeFor[ConfigPatch](),
	"ConfigSaved":              reflect.TypeFor[ConfigSaved](),
	"ResumeUploaded":           reflect.TypeFor[ResumeUploaded](),
	"ResumeEntry":              reflect.TypeFor[ResumeEntry](),
	"ResumeList":               reflect.TypeFor[ResumeList](),
	"ResumeRankRequest":        reflect.TypeFor[ResumeRankRequest](),
	"ResumeRankItem":           reflect.TypeFor[ResumeRankItem](),
	"ResumeRank":               reflect.TypeFor[ResumeRank](),
	"SubmissionRecord":         reflect.TypeFor[SubmissionRecord](),
	"SubmissionRecorded":       reflect.TypeFor[SubmissionRecorded](),
	"PressClaimRequest":        reflect.TypeFor[PressClaimRequest](),
	"PressClaimGranted":        reflect.TypeFor[PressClaimGranted](),
	"SigninResolveRequest":     reflect.TypeFor[SigninResolveRequest](),
	"SigninCredential":         reflect.TypeFor[SigninCredential](),
	"SecretRegistryRow":        reflect.TypeFor[SecretRegistryRow](),
	"SecretEntry":              reflect.TypeFor[SecretEntry](),
	"SecretList":               reflect.TypeFor[SecretList](),
	"SecretWrite":              reflect.TypeFor[SecretWrite](),
	"SecretStatus":             reflect.TypeFor[SecretStatus](),
	"AlternateWrite":           reflect.TypeFor[AlternateWrite](),
	"AlternateAdded":           reflect.TypeFor[AlternateAdded](),
	"AlternateOrder":           reflect.TypeFor[AlternateOrder](),
	"SecretTest":               reflect.TypeFor[SecretTest](),
	"OneTimeCode":              reflect.TypeFor[OneTimeCode](),
	"RiskView":                 reflect.TypeFor[RiskView](),
	"SourceFunnel":             reflect.TypeFor[SourceFunnel](),
	"FunnelWeek":               reflect.TypeFor[FunnelWeek](),
	"GmailStage":               reflect.TypeFor[GmailStage](),
	"GmailCounts":              reflect.TypeFor[GmailCounts](),
	"GmailRun":                 reflect.TypeFor[GmailRun](),
	"GmailRunList":             reflect.TypeFor[GmailRunList](),
	"GmailHealth":              reflect.TypeFor[GmailHealth](),
	"GmailAccountLink":         reflect.TypeFor[GmailAccountLink](),
	"GmailSigninLink":          reflect.TypeFor[GmailSigninLink](),
	"GmailInboxThread":         reflect.TypeFor[GmailInboxThread](),
	"GmailInboxPage":           reflect.TypeFor[GmailInboxPage](),
	"GmailTimelineEntry":       reflect.TypeFor[GmailTimelineEntry](),
	"GmailThreadDetail":        reflect.TypeFor[GmailThreadDetail](),
	"GmailSummary":             reflect.TypeFor[GmailSummary](),
	"GmailThreadDraft":         reflect.TypeFor[GmailThreadDraft](),
	"GmailLinkItem":            reflect.TypeFor[GmailLinkItem](),
	"GmailOutcome":             reflect.TypeFor[GmailOutcome](),
	"GmailLinkList":            reflect.TypeFor[GmailLinkList](),
	"GmailLinkConfirm":         reflect.TypeFor[GmailLinkConfirm](),
	"GmailLinkConfirmed":       reflect.TypeFor[GmailLinkConfirmed](),
	"GmailLinkRejected":        reflect.TypeFor[GmailLinkRejected](),
	"GmailLinkRestored":        reflect.TypeFor[GmailLinkRestored](),
	"GmailThreadDone":          reflect.TypeFor[GmailThreadDone](),
	"GmailThreadDismissed":     reflect.TypeFor[GmailThreadDismissed](),
	"GmailConfirmation":        reflect.TypeFor[GmailConfirmation](),
	"GmailConfirmationList":    reflect.TypeFor[GmailConfirmationList](),
	"GmailMoveList":            reflect.TypeFor[GmailMoveList](),
	"GmailConfirmationDecided": reflect.TypeFor[GmailConfirmationDecided](),
	"GmailDigestItem":          reflect.TypeFor[GmailDigestItem](),
	"GmailDigest":              reflect.TypeFor[GmailDigest](),
	"GmailDigestList":          reflect.TypeFor[GmailDigestList](),
	"GmailDigestVerdicts":      reflect.TypeFor[GmailDigestVerdicts](),
	"GmailDigestFeedback":      reflect.TypeFor[GmailDigestFeedback](),
	"GmailDigestRated":         reflect.TypeFor[GmailDigestRated](),
	"GmailSettings":            reflect.TypeFor[GmailSettings](),
	"GmailSettingsPatch":       reflect.TypeFor[GmailSettingsPatch](),
	"GmailSyncStarted":         reflect.TypeFor[GmailSyncStarted](),
	"GmailConnect":             reflect.TypeFor[GmailConnect](),
	"GmailConnected":           reflect.TypeFor[GmailConnected](),
	"GmailTest":                reflect.TypeFor[GmailTest](),
	"SchedulePreset":           reflect.TypeFor[SchedulePreset](),
	"Schedule":                 reflect.TypeFor[Schedule](),
	"ScheduleTimer":            reflect.TypeFor[ScheduleTimer](),
	"ScheduleList":             reflect.TypeFor[ScheduleList](),
	"SchedulePauseResult":      reflect.TypeFor[SchedulePauseResult](),
	"ScheduleCreate":           reflect.TypeFor[ScheduleCreate](),
	"SchedulePatch":            reflect.TypeFor[SchedulePatch](),
	"ScheduleField":            reflect.TypeFor[ScheduleField](),
	"ScheduleKind":             reflect.TypeFor[ScheduleKind](),
	"ScheduleKindList":         reflect.TypeFor[ScheduleKindList](),
	"ScheduleHistoryRow":       reflect.TypeFor[ScheduleHistoryRow](),
	"ScheduleHistory":          reflect.TypeFor[ScheduleHistory](),
	"SchedulePackEntry":        reflect.TypeFor[SchedulePackEntry](),
	"SchedulePack":             reflect.TypeFor[SchedulePack](),
	"SchedulePackList":         reflect.TypeFor[SchedulePackList](),
	"SchedulePackUnavailable":  reflect.TypeFor[SchedulePackUnavailable](),
	"SchedulePackResult":       reflect.TypeFor[SchedulePackResult](),
	"SchedulePackDeleted":      reflect.TypeFor[SchedulePackDeleted](),
	"KBLint":                   reflect.TypeFor[KBLint](),
	"KBDocEntry":               reflect.TypeFor[KBDocEntry](),
	"KBDocumentList":           reflect.TypeFor[KBDocumentList](),
	"KBSection":                reflect.TypeFor[KBSection](),
	"KBDocument":               reflect.TypeFor[KBDocument](),
	"KBSnippetCreate":          reflect.TypeFor[KBSnippetCreate](),
	"KBDocumentRevise":         reflect.TypeFor[KBDocumentRevise](),
	"KBPrevious":               reflect.TypeFor[KBPrevious](),
	"KBBriefRequest":           reflect.TypeFor[KBBriefRequest](),
	"KBStoryCreate":            reflect.TypeFor[KBStoryCreate](),
	"KBStoryUse":               reflect.TypeFor[KBStoryUse](),
	"KBStoryFinding":           reflect.TypeFor[KBStoryFinding](),
	"KBStorySaved":             reflect.TypeFor[KBStorySaved](),
	"KBStoryProposal":          reflect.TypeFor[KBStoryProposal](),
	"KBStoryProposals":         reflect.TypeFor[KBStoryProposals](),
	"KBStoryProposalRef":       reflect.TypeFor[KBStoryProposalRef](),

	"AnswerBankGap":                 reflect.TypeFor[AnswerBankGap](),
	"AnswerBankUnanswered":          reflect.TypeFor[AnswerBankUnanswered](),
	"AnswerBankAsked":               reflect.TypeFor[AnswerBankAsked](),
	"AnswerBankSettings":            reflect.TypeFor[AnswerBankSettings](),
	"AnswerBankSettingsPatch":       reflect.TypeFor[AnswerBankSettingsPatch](),
	"AnswerBankTranscript":          reflect.TypeFor[AnswerBankTranscript](),
	"AnswerBankBody":                reflect.TypeFor[AnswerBankBody](),
	"AnswerBankNumericThreshold":    reflect.TypeFor[AnswerBankNumericThreshold](),
	"AnswerBankLocationPolicy":      reflect.TypeFor[AnswerBankLocationPolicy](),
	"AnswerBankScopeItem":           reflect.TypeFor[AnswerBankScopeItem](),
	"AnswerBankEntry":               reflect.TypeFor[AnswerBankEntry](),
	"AnswerBankEmployer":            reflect.TypeFor[AnswerBankEmployer](),
	"AnswerBankOverride":            reflect.TypeFor[AnswerBankOverride](),
	"AnswerBank":                    reflect.TypeFor[AnswerBank](),
	"AnswerBankFile":                reflect.TypeFor[AnswerBankFile](),
	"AnswerBankShape":               reflect.TypeFor[AnswerBankShape](),
	"AnswerBankChoice":              reflect.TypeFor[AnswerBankChoice](),
	"AnswerBankSave":                reflect.TypeFor[AnswerBankSave](),
	"AnswerBankSaved":               reflect.TypeFor[AnswerBankSaved](),
	"LearningReport":                reflect.TypeFor[LearningReport](),
	"LearningEvent":                 reflect.TypeFor[LearningEvent](),
	"LearningEventsRequest":         reflect.TypeFor[LearningEventsRequest](),
	"SystemWriteRequest":            reflect.TypeFor[SystemWriteRequest](),
	"ApplicantSnapshot":             reflect.TypeFor[ApplicantSnapshot](),
	"ApplicantCandidate":            reflect.TypeFor[ApplicantCandidate](),
	"ApplicantAddress":              reflect.TypeFor[ApplicantAddress](),
	"ApplicantLinks":                reflect.TypeFor[ApplicantLinks](),
	"ApplicantHistory":              reflect.TypeFor[ApplicantHistory](),
	"ApplicantJob":                  reflect.TypeFor[ApplicantJob](),
	"ApplicantStudy":                reflect.TypeFor[ApplicantStudy](),
	"ExtensionBuild":                reflect.TypeFor[ExtensionBuild](),
	"ExtensionClient":               reflect.TypeFor[ExtensionClient](),
	"ExtensionClients":              reflect.TypeFor[ExtensionClients](),
	"PostingRead":                   reflect.TypeFor[PostingRead](),
	"FillRunRequest":                reflect.TypeFor[FillRunRequest](),
	"FillRunEnd":                    reflect.TypeFor[FillRunEnd](),
	"SessionSiteCount":              reflect.TypeFor[SessionSiteCount](),
	"SessionCheckin":                reflect.TypeFor[SessionCheckin](),
	"SessionPending":                reflect.TypeFor[SessionPending](),
	"SessionCheckinReply":           reflect.TypeFor[SessionCheckinReply](),
	"SessionCookie":                 reflect.TypeFor[SessionCookie](),
	"SessionImport":                 reflect.TypeFor[SessionImport](),
	"SessionPush":                   reflect.TypeFor[SessionPush](),
	"SessionImportResult":           reflect.TypeFor[SessionImportResult](),
	"SessionSyncRequest":            reflect.TypeFor[SessionSyncRequest](),
	"BrowserOpenRequest":            reflect.TypeFor[BrowserOpenRequest](),
	"BrowserOpened":                 reflect.TypeFor[BrowserOpened](),
	"BrowserStatus":                 reflect.TypeFor[BrowserStatus](),
	"BrowserShowRequest":            reflect.TypeFor[BrowserShowRequest](),
	"BrowserShown":                  reflect.TypeFor[BrowserShown](),
	"BrowserStopRequest":            reflect.TypeFor[BrowserStopRequest](),
	"BrowserStopped":                reflect.TypeFor[BrowserStopped](),
	"BrowserInputRequest":           reflect.TypeFor[BrowserInputRequest](),
	"BrowserNavigateRequest":        reflect.TypeFor[BrowserNavigateRequest](),
	"BrowserTabSent":                reflect.TypeFor[BrowserTabSent](),
	"BrowserNewTabRequest":          reflect.TypeFor[BrowserNewTabRequest](),
	"BrowserNewTab":                 reflect.TypeFor[BrowserNewTab](),
	"BrowserLiveFrame":              reflect.TypeFor[BrowserLiveFrame](),
	"BrowserLiveTab":                reflect.TypeFor[BrowserLiveTab](),
	"BrowserLiveCursor":             reflect.TypeFor[BrowserLiveCursor](),
	"BrowserLiveFileChooser":        reflect.TypeFor[BrowserLiveFileChooser](),
	"BrowserLiveEnd":                reflect.TypeFor[BrowserLiveEnd](),
	"SignInCheck":                   reflect.TypeFor[SignInCheck](),
	"SignInChecks":                  reflect.TypeFor[SignInChecks](),
	"SessionUndoResult":             reflect.TypeFor[SessionUndoResult](),
	"SessionSiteView":               reflect.TypeFor[SessionSiteView](),
	"SessionOutcome":                reflect.TypeFor[SessionOutcome](),
	"SessionImportSummary":          reflect.TypeFor[SessionImportSummary](),
	"SessionsView":                  reflect.TypeFor[SessionsView](),
	"ImportSources":                 reflect.TypeFor[ImportSources](),
	"ImportSites":                   reflect.TypeFor[ImportSites](),
	"ImportRequest":                 reflect.TypeFor[ImportRequest](),
	"ImportedView":                  reflect.TypeFor[ImportedView](),
	"ForgetRequest":                 reflect.TypeFor[ForgetRequest](),
	"ForgetResult":                  reflect.TypeFor[ForgetResult](),
	"SystemWriteRecorded":           reflect.TypeFor[SystemWriteRecorded](),
	"LearningEventRejection":        reflect.TypeFor[LearningEventRejection](),
	"LearningEventsResult":          reflect.TypeFor[LearningEventsResult](),
	"ManualFill":                    reflect.TypeFor[ManualFill](),
	"ManualFillList":                reflect.TypeFor[ManualFillList](),
	"ManualFillDeleted":             reflect.TypeFor[ManualFillDeleted](),
	"AnswerEdit":                    reflect.TypeFor[AnswerEdit](),
	"AnswerCandidate":               reflect.TypeFor[AnswerCandidate](),
	"AnswerCandidateList":           reflect.TypeFor[AnswerCandidateList](),
	"AnswerCandidateGenerate":       reflect.TypeFor[AnswerCandidateGenerate](),
	"AnswerCandidateGenerateResult": reflect.TypeFor[AnswerCandidateGenerateResult](),
	"AnswerCandidateApprove":        reflect.TypeFor[AnswerCandidateApprove](),
	"AnswerCandidateDecision":       reflect.TypeFor[AnswerCandidateDecision](),
	"AnswerBankUsageRow":            reflect.TypeFor[AnswerBankUsageRow](),
	"AnswerBankUsage":               reflect.TypeFor[AnswerBankUsage](),
	"AnswerComposePosting":          reflect.TypeFor[AnswerComposePosting](),
	"AnswerComposeRequest":          reflect.TypeFor[AnswerComposeRequest](),
	"AnswerComposeResult":           reflect.TypeFor[AnswerComposeResult](),
	"AnswerMatchOption":             reflect.TypeFor[AnswerMatchOption](),
	"AnswerMatchQuestion":           reflect.TypeFor[AnswerMatchQuestion](),
	"AnswerMatchPosting":            reflect.TypeFor[AnswerMatchPosting](),
	"AnswerMatchRequest":            reflect.TypeFor[AnswerMatchRequest](),
	"AnswerMatchItem":               reflect.TypeFor[AnswerMatchItem](),
	"AnswerMatchResult":             reflect.TypeFor[AnswerMatchResult](),
	"DocumentResumeRequest":         reflect.TypeFor[DocumentResumeRequest](),
	"DocumentCoverRequest":          reflect.TypeFor[DocumentCoverRequest](),
	"DocumentPDFRequest":            reflect.TypeFor[DocumentPDFRequest](),
	"DocumentView":                  reflect.TypeFor[DocumentView](),
	"DocumentCheck":                 reflect.TypeFor[DocumentCheck](),
	"DocumentPreview":               reflect.TypeFor[DocumentPreview](),
	"DocumentVisualReview":          reflect.TypeFor[DocumentVisualReview](),
	"DocumentVerifiers":             reflect.TypeFor[DocumentVerifiers](),
	"DocumentVerification":          reflect.TypeFor[DocumentVerification](),
	"DocumentList":                  reflect.TypeFor[DocumentList](),
	"DocumentPreferences":           reflect.TypeFor[DocumentPreferences](),
	"DocumentPreferencesPatch":      reflect.TypeFor[DocumentPreferencesPatch](),
	"DocumentEditRequest":           reflect.TypeFor[DocumentEditRequest](),
	"DocumentSource":                reflect.TypeFor[DocumentSource](),
	"DocumentEvidence":              reflect.TypeFor[DocumentEvidence](),
	"DocumentRefineRequest":         reflect.TypeFor[DocumentRefineRequest](),
	"DocumentRefineDecision":        reflect.TypeFor[DocumentRefineDecision](),
	"DocumentRefinement":            reflect.TypeFor[DocumentRefinement](),
	"DocumentRetrieval":             reflect.TypeFor[DocumentRetrieval](),
	"DocumentRunView":               reflect.TypeFor[DocumentRunView](),
	"InterviewStageRequest":         reflect.TypeFor[InterviewStageRequest](),
	"InterviewStage":                reflect.TypeFor[InterviewStage](),
	"InterviewPackRequest":          reflect.TypeFor[InterviewPackRequest](),
	"InterviewFeedbackRequest":      reflect.TypeFor[InterviewFeedbackRequest](),
	"InterviewFeedback":             reflect.TypeFor[InterviewFeedback](),
	"InterviewMockTurnRequest":      reflect.TypeFor[InterviewMockTurnRequest](),
	"InterviewMockTurn":             reflect.TypeFor[InterviewMockTurn](),
	"InterviewSourceRef":            reflect.TypeFor[InterviewSourceRef](),
	"InterviewHook":                 reflect.TypeFor[InterviewHook](),
	"InterviewQuestion":             reflect.TypeFor[InterviewQuestion](),
	"InterviewPack":                 reflect.TypeFor[InterviewPack](),
	"InterviewPackRef":              reflect.TypeFor[InterviewPackRef](),
	"InterviewPackInfo":             reflect.TypeFor[InterviewPackInfo](),
	"InterviewWorkspace":            reflect.TypeFor[InterviewWorkspace](),
	"InterviewsSoon":                reflect.TypeFor[InterviewsSoon](),
	"AgentToolSpec":                 reflect.TypeFor[AgentToolSpec](),
	"AgentCatalog":                  reflect.TypeFor[AgentCatalog](),
	"AgentHello":                    reflect.TypeFor[AgentHello](),
	"AgentHelloResult":              reflect.TypeFor[AgentHelloResult](),
	"AgentToolCall":                 reflect.TypeFor[AgentToolCall](),
	"AgentToolResult":               reflect.TypeFor[AgentToolResult](),
	"AgentPrefixMessage":            reflect.TypeFor[AgentPrefixMessage](),
	"AgentTurnContext":              reflect.TypeFor[AgentTurnContext](),
	"ChatSession":                   reflect.TypeFor[ChatSession](),
	"ChatSessionPage":               reflect.TypeFor[ChatSessionPage](),
	"ChatMessage":                   reflect.TypeFor[ChatMessage](),
	"ChatTurn":                      reflect.TypeFor[ChatTurn](),
	"ChatSessionDetail":             reflect.TypeFor[ChatSessionDetail](),
	"ChatRename":                    reflect.TypeFor[ChatRename](),
	"ChatSend":                      reflect.TypeFor[ChatSend](),
	"ChatTurnAccepted":              reflect.TypeFor[ChatTurnAccepted](),
	"ChatSteer":                     reflect.TypeFor[ChatSteer](),
	"ChatSteerAccepted":             reflect.TypeFor[ChatSteerAccepted](),
	"ChatCancelResult":              reflect.TypeFor[ChatCancelResult](),
	"ChatFork":                      reflect.TypeFor[ChatFork](),
	"ChatDeleted":                   reflect.TypeFor[ChatDeleted](),
	"ChatEvent":                     reflect.TypeFor[ChatEvent](),
	"ChatCitation":                  reflect.TypeFor[ChatCitation](),
	"ConfirmationCard":              reflect.TypeFor[ConfirmationCard](),
	"ConfirmationList":              reflect.TypeFor[ConfirmationList](),
	"ConfirmationOutcome":           reflect.TypeFor[ConfirmationOutcome](),
	"ApproveStanding":               reflect.TypeFor[ApproveStanding](),
	"StandingApproval":              reflect.TypeFor[StandingApproval](),
	"StandingApprovalList":          reflect.TypeFor[StandingApprovalList](),
	"StandingApprovalRevoked":       reflect.TypeFor[StandingApprovalRevoked](),
	"Goal":                          reflect.TypeFor[Goal](),
	"GoalList":                      reflect.TypeFor[GoalList](),
	"AgentTask":                     reflect.TypeFor[AgentTask](),
	"AgentTaskList":                 reflect.TypeFor[AgentTaskList](),
	"AgentIdeaStarted":              reflect.TypeFor[AgentIdeaStarted](),
	"GoalCreate":                    reflect.TypeFor[GoalCreate](),
	"GoalPatch":                     reflect.TypeFor[GoalPatch](),
	"GoalDeleted":                   reflect.TypeFor[GoalDeleted](),
	"ScheduleProposal":              reflect.TypeFor[ScheduleProposal](),
	"ScheduleProposalList":          reflect.TypeFor[ScheduleProposalList](),
	"ScheduleProposalDecided":       reflect.TypeFor[ScheduleProposalDecided](),
	"MemoryFact":                    reflect.TypeFor[MemoryFact](),
	"MemoryFactList":                reflect.TypeFor[MemoryFactList](),
	"MemoryFactPatch":               reflect.TypeFor[MemoryFactPatch](),
	"MemoryFactEdited":              reflect.TypeFor[MemoryFactEdited](),
	"MemoryRule":                    reflect.TypeFor[MemoryRule](),
	"MemoryRuleList":                reflect.TypeFor[MemoryRuleList](),
	"MemoryRulePatch":               reflect.TypeFor[MemoryRulePatch](),
	"MemoryReviewItem":              reflect.TypeFor[MemoryReviewItem](),
	"MemoryReviewList":              reflect.TypeFor[MemoryReviewList](),
	"MemoryDeleted":                 reflect.TypeFor[MemoryDeleted](),
	"MemoryExport":                  reflect.TypeFor[MemoryExport](),
	"AgentSettings":                 reflect.TypeFor[AgentSettings](),
	"AgentSettingsPatch":            reflect.TypeFor[AgentSettingsPatch](),
	"AgentHarness":                  reflect.TypeFor[AgentHarness](),
	"AgentHarnessRestore":           reflect.TypeFor[AgentHarnessRestore](),
	"AgentPrompt":                   reflect.TypeFor[AgentPrompt](),
	"AgentPromptList":               reflect.TypeFor[AgentPromptList](),
	"AgentPromptCreate":             reflect.TypeFor[AgentPromptCreate](),
	"AgentPromptPatch":              reflect.TypeFor[AgentPromptPatch](),
	"AgentPromptDeleted":            reflect.TypeFor[AgentPromptDeleted](),
	"BoardEntry":                    reflect.TypeFor[BoardEntry](),
	"BoardEntryList":                reflect.TypeFor[BoardEntryList](),
	"BoardSummary":                  reflect.TypeFor[BoardSummary](),
	"NeedsYouItem":                  reflect.TypeFor[NeedsYouItem](),
	"NeedsYouInbox":                 reflect.TypeFor[NeedsYouInbox](),
	"NeedsYouCounts":                reflect.TypeFor[NeedsYouCounts](),
	"DayView":                       reflect.TypeFor[DayView](),
	"DayLine":                       reflect.TypeFor[DayLine](),
	"DayCounts":                     reflect.TypeFor[DayCounts](),
	"NotificationSettings":          reflect.TypeFor[NotificationSettings](),
	"NotificationSettingsPatch":     reflect.TypeFor[NotificationSettingsPatch](),
	"NotificationTest":              reflect.TypeFor[NotificationTest](),
	"NotificationAnnounce":          reflect.TypeFor[NotificationAnnounce](),
	"NotificationWithdraw":          reflect.TypeFor[NotificationWithdraw](),
	"Notice":                        reflect.TypeFor[Notice](),
	"NoticeList":                    reflect.TypeFor[NoticeList](),
	"NoticesRead":                   reflect.TypeFor[NoticesRead](),
	"TelegramStatus":                reflect.TypeFor[TelegramStatus](),
	"TelegramTokenRequest":          reflect.TypeFor[TelegramTokenRequest](),
	"TelegramPairingCode":           reflect.TypeFor[TelegramPairingCode](),
	"ComputerUseStatus":             reflect.TypeFor[ComputerUseStatus](),
	"ComputerUsePause":              reflect.TypeFor[ComputerUsePause](),
	"DesktopActivity":               reflect.TypeFor[DesktopActivity](),
}

// The union of notification stream events for clients.
//
//tygo:emit
var notificationEventTS = `export type NotificationStreamEvent =
  | { event: "announce"; data: NotificationAnnounce }
  | { event: "withdraw"; data: NotificationWithdraw }
  | { event: "reset"; data: StreamReset }
  | { event: "shutdown"; data: StreamShutdown };`

// The union of stream events for clients; a test compares it with Events.
//
//tygo:emit
var streamEventTS = `export type StreamEvent =
  | { event: "snapshot"; data: BoardSnapshot }
  | { event: "notice"; data: BoardNotice }
  | { event: "reset"; data: StreamReset }
  | { event: "shutdown"; data: StreamShutdown }
  | { event: "state"; data: RunView }
  | { event: "phase"; data: RunPhase }
  | { event: "progress"; data: RunProgress }
  | { event: "final"; data: RunFinal }
  | { event: "chat"; data: ChatEvent };`

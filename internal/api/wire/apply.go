package wire

// Run kind and reasons of the fill of the active tab.
const (
	RunKindAutofillTab = "autofill-tab"

	ReasonNoTab          = "no-tab"
	ReasonNotAnApp       = "not-an-application"
	ReasonLaneBusy       = "lane-busy"
	ReasonAlreadyRunning = "already-running"
)

// AutofillControl is one control of a fill's result: its label and, for a
// control that was not filled, the reason. It never holds a value.
type AutofillControl struct {
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
}

// AutofillTabResult is what a fill of the active tab did. Status is the run's
// status; the groups are empty until the run ends.
type AutofillTabResult struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	// Reason says why a failed run failed.
	Reason  string            `json:"reason,omitempty"`
	URL     string            `json:"url,omitempty"`
	Site    string            `json:"site,omitempty"`
	Filled  []AutofillControl `json:"filled"`
	Review  []AutofillControl `json:"review"`
	Missing []AutofillControl `json:"missing"`
	Skipped []AutofillControl `json:"skipped"`
	// Drafts counts composed answers held back for review, Flagged the written
	// values flagged for review and Events the learning events accepted.
	Drafts  SafeInt `json:"drafts"`
	Flagged SafeInt `json:"flagged"`
	Events  SafeInt `json:"events"`
}

// ApplyRunRequest starts one application run. Submit false is a dry run that
// fills every page and presses nothing; Submit true needs Confirmed, which the
// dashboard sets only after its confirmation dialog.
type ApplyRunRequest struct {
	URL       string `json:"url"`
	Company   string `json:"company,omitempty"`
	Role      string `json:"role,omitempty"`
	Submit    bool   `json:"submit"`
	Confirmed bool   `json:"confirmed"`
	// Resubmit re-sends an application the ledger holds as unconfirmed; it
	// needs Submit and is the command line's `--resubmit`.
	Resubmit bool `json:"resubmit,omitempty"`
}

// ApplyBatchRequest starts a run over the rows of lanes, in lane order, at
// most Limit of them.
type ApplyBatchRequest struct {
	Lanes []string `json:"lanes"`
	Limit SafeInt  `json:"limit"`
	// Companies limits the list to the rows of the companies the user named,
	// whole word and case-insensitive; empty runs the lanes' top rows.
	Companies []string `json:"companies,omitempty"`
	// RetryToday attempts the rows that stopped today on a judgement; it never
	// lifts a duplicate, the skip list or a hold on a fact about the posting.
	RetryToday bool `json:"retryToday,omitempty"`
	Submit     bool `json:"submit"`
	Confirmed  bool `json:"confirmed"`
	// Candidacies runs exactly these tracker rows of Apply now and Selective,
	// 1 to 100, instead of the lanes' top rows; it cannot be combined with
	// Companies, and Lanes and Limit are then not read.
	Candidacies []SafeInt `json:"candidacies,omitempty"`
}

// ApplySkippedRow is a row a batch left to the user, with the reason.
type ApplySkippedRow struct {
	Company string `json:"company"`
	Role    string `json:"role"`
	Reason  string `json:"reason"`
}

// ApplyBatchAccepted answers a batch start: the first row's run id, how many
// rows run and the rows left for the user.
type ApplyBatchAccepted struct {
	RunID   string            `json:"runId"`
	BatchID string            `json:"batchId"`
	Rows    SafeInt           `json:"rows"`
	Skipped []ApplySkippedRow `json:"skipped"`
	// Ahead is set when the start waits for the apply window: how many starts
	// go before it, the one holding the window included.
	Ahead SafeInt `json:"ahead,omitempty"`
}

// Run modes: a dry run presses nothing and buys no captcha solve; it may still
// sign in, create the account the application needs behind the account gates,
// upload and save a draft on the live site.
const (
	ApplyModeDryRun = "dry-run"
	ApplyModeSubmit = "submit"
)

// Batch states.
const (
	BatchOpen   = "open"
	BatchClosed = "closed"
)

// BatchRowStarted is the journal state of a batch row whose run began.
const BatchRowStarted = "started"

// Owners of a stopped row: who has the next step.
const (
	OwnerUser        = "user"
	OwnerPosting     = "posting"
	OwnerEnvironment = "environment"
	OwnerDefect      = "defect"
)

// ApplyOutcome is the verdict of a run. It holds no form value.
type ApplyOutcome struct {
	Kind      string `json:"kind"`
	Stage     string `json:"stage,omitempty"`
	Class     string `json:"class,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
	// Owner says who has the next step of a stop; empty for a run that ended
	// filled or submitted.
	Owner string `json:"owner,omitempty"`
}

// ApplyStage is one stage line of a run with its duration.
type ApplyStage struct {
	Stage     string  `json:"stage"`
	Status    string  `json:"status"`
	Detail    string  `json:"detail"`
	StartedAt Time    `json:"startedAt"`
	Ms        SafeInt `json:"ms"`
}

// ApplyRunView answers GET /api/apply/runs/{id}: the record of one run.
type ApplyRunView struct {
	RunID   string `json:"runId"`
	BatchID string `json:"batchId,omitempty"`
	Mode    string `json:"mode"`
	Caller  string `json:"caller"`
	Company string `json:"company,omitempty"`
	Role    string `json:"role,omitempty"`
	// Host is the posting's host; the address itself is not returned.
	Host       string       `json:"host"`
	ATS        string       `json:"ats,omitempty"`
	StartedAt  Time         `json:"startedAt"`
	FinishedAt *Time        `json:"finishedAt,omitempty"`
	Outcome    ApplyOutcome `json:"outcome"`
	Stages     []ApplyStage `json:"stages"`
	Pages      SafeInt      `json:"pages"`
	// ReportNumber and CandidacyID link the run to its report and tracker row.
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	CandidacyID  *SafeInt `json:"candidacyId,omitempty"`
	// Uploads are the required upload slots a stopped run left empty, by
	// label: a file is no Answer Bank entry, so the questions leave it out.
	Uploads []string `json:"uploads,omitempty"`
	// LetterOverride is true when a stopped run left a cover-letter slot empty,
	// its final report is below the 4.5 document floor with no override and
	// the document settings write an asked-for letter only above the floor:
	// the user's override for that report and a retry can fill it.
	LetterOverride bool `json:"letterOverride,omitempty"`
	// KeptTab is true while a tab the run kept for the user is open.
	KeptTab bool `json:"keptTab"`
	// Correction is offered on a submitted run only when the correction door
	// would accept its message today.
	Correction *ApplyCorrection `json:"correction,omitempty"`
	// Questions are what a stopped run left for the user that an entry can answer.
	Questions []ApplyQuestion `json:"questions,omitempty"`
	// Edits are the run's edits the keep press is offered for.
	Edits []AnswerEdit `json:"edits,omitempty"`
}

// ApplyQuestion is one such question; AnsweredBy names the entry that fills it
// now. Everywhere is where the user's answer applies unless they choose
// otherwise: every site, unless the question names the company (F-4).
// Draftable is true for a free-text question the composer would draft an
// answer to (POST /api/answers/compose); a question that tells the applicant
// to write it themselves, and a choice, never is.
type ApplyQuestion struct {
	Question   string   `json:"question"`
	Kind       string   `json:"kind"`
	Options    []string `json:"options,omitempty"`
	AnsweredBy string   `json:"answeredBy,omitempty"`
	Answer     string   `json:"answer,omitempty"`
	Everywhere bool     `json:"everywhere"`
	Draftable  bool     `json:"draftable,omitempty"`
}

// ApplyAnswerRequest saves the user's answer to one question as their own
// entry; Everywhere drops its scope to the run's host.
type ApplyAnswerRequest struct {
	Question   string `json:"question"`
	Answer     string `json:"answer"`
	Everywhere bool   `json:"everywhere"`
}

// ApplyAnswerSaved names the entry written. Continued says the answer was the
// last one the run waited for and the run went on in the tab it kept.
type ApplyAnswerSaved struct {
	EntryID   string `json:"entryId"`
	Continued bool   `json:"continued,omitempty"`
}

// ApplyCorrection is the request that corrects a submitted application: the
// exact message the user sends to the Ask Agent. The dashboard sends nothing
// itself.
type ApplyCorrection struct {
	Message string `json:"message"`
}

// ApplyRetryRequest starts the same posting again. Submit true needs Confirmed.
type ApplyRetryRequest struct {
	Submit    bool `json:"submit"`
	Confirmed bool `json:"confirmed"`
}

// ApplySubmitRequest is "Submit this form" on a run whose filled form waits at
// Submit in its kept tab; it needs Confirmed, set only after the dialog that
// names the company, the role and the host.
type ApplySubmitRequest struct {
	Confirmed bool `json:"confirmed"`
}

// ApplyBatchRow is one listed posting of a batch.
type ApplyBatchRow struct {
	Index   SafeInt `json:"index"`
	Company string  `json:"company"`
	Role    string  `json:"role"`
	Host    string  `json:"host"`
	Mirrors SafeInt `json:"mirrors,omitempty"`
	State   string  `json:"state"`
	RunID   string  `json:"runId,omitempty"`
	// Note says why a row has no run: the skip rule, the time budget, an
	// aborted list.
	Note    string        `json:"note,omitempty"`
	Outcome *ApplyOutcome `json:"outcome,omitempty"`
}

// ApplyDefectShape is one distinct defect of a batch (stage, class, reason with
// its digits collapsed), listed once with its companies and the newest
// snapshot.
type ApplyDefectShape struct {
	Stage     string   `json:"stage"`
	Class     string   `json:"class"`
	Reason    string   `json:"reason"`
	Companies []string `json:"companies"`
	Snapshot  string   `json:"snapshot,omitempty"`
}

// ApplyOwnerGroup lists the stopped rows an owner has the next step of.
type ApplyOwnerGroup struct {
	Owner     string   `json:"owner"`
	Companies []string `json:"companies"`
}

// ApplyBatchView answers GET /api/apply/batches/{id}.
type ApplyBatchView struct {
	BatchID   string             `json:"batchId"`
	State     string             `json:"state"`
	Mode      string             `json:"mode"`
	Caller    string             `json:"caller"`
	CreatedAt Time               `json:"createdAt"`
	Rows      []ApplyBatchRow    `json:"rows"`
	Owners    []ApplyOwnerGroup  `json:"owners"`
	Defects   []ApplyDefectShape `json:"defects"`
}

// ApplySkipList is the batch skip list. Defaulted is true while the profile
// has no list of its own and the built-in names apply; Differs is true when
// the effective list is not the built-in one. Revision guards the write.
type ApplySkipList struct {
	Entries   []string `json:"entries"`
	Defaulted bool     `json:"defaulted"`
	Differs   bool     `json:"differs"`
	Defaults  []string `json:"defaults"`
	Revision  string   `json:"revision"`
}

// ApplySkipListWrite replaces the whole list. Revision is the one the client
// read; a profile that changed since is a conflict.
type ApplySkipListWrite struct {
	Entries  []string `json:"entries"`
	Revision string   `json:"revision"`
}

// ApplyTabShown answers the route that brings a run's kept tab forward.
type ApplyTabShown struct {
	RunID string `json:"runId"`
}

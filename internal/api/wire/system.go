package wire

// LLMTestResult is the saved outcome of a provider test call. It holds no
// prompt and no response text. Outcome is ok or failed; ErrorClass is auth,
// rate-limit, billing, network, model-not-found, timeout, setup or other, and Fix is
// the one-line instruction for it.
type LLMTestResult struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Outcome    string   `json:"outcome"`
	ErrorClass string   `json:"errorClass,omitempty"`
	Fix        string   `json:"fix,omitempty"`
	LatencyMs  SafeInt  `json:"latencyMs"`
	TestedAt   Time     `json:"testedAt"`
	CostUSD    *float64 `json:"costUsd,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// LLMTestRequest optionally names the model a provider test sends; empty
// tests the provider's usual model. The body itself may be empty.
type LLMTestRequest struct {
	Model string `json:"model,omitempty"`
}

// LLMTestStarted says a provider test is running. The result arrives in the
// routing view as lastTest when it ends.
type LLMTestStarted struct {
	Provider string `json:"provider"`
}

// SystemInfo is what the Files and Advanced sections need beyond health.
type SystemInfo struct {
	WorkspacePath string `json:"workspacePath"`
}

// LogLines are the newest lines of the server log, already redacted.
type LogLines struct {
	Lines []string `json:"lines"`
}

// CLIInstall is the state of the applysmith command on the user's PATH: the link
// ~/.local/bin/applysmith to the binary inside the installed app. State is
// not-installed, installed, stale (a link to an app binary elsewhere) or
// foreign (a different file is there and is never touched). Available is false
// when the server does not run from an installed app. ProfileFile and
// ProfileLine are the one line to add to the shell profile when the folder is
// not on PATH; the server never edits the profile.
type CLIInstall struct {
	Available   bool   `json:"available"`
	State       string `json:"state"`
	Link        string `json:"link"`
	Target      string `json:"target"`
	OnPath      bool   `json:"onPath"`
	ProfileFile string `json:"profileFile"`
	ProfileLine string `json:"profileLine"`
}

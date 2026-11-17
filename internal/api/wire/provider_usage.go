package wire

// ProviderAccounts describes native host installations and optional subscription
// monitors. Account credentials and file contents are never returned.
type ProviderAccounts struct {
	Accounts []ProviderAccount `json:"accounts"`
}
type ProviderAccount struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Mode                string        `json:"mode"`
	Installed           bool          `json:"installed"`
	AuthFilePresent     bool          `json:"authFilePresent"`
	CredentialStatus    string        `json:"credentialStatus"`
	KeyConfigured       bool          `json:"keyConfigured"`
	CookieConfigured    bool          `json:"cookieConfigured"`
	CookieSupported     bool          `json:"cookieSupported"`
	WorkspaceConfigured bool          `json:"workspaceConfigured"`
	LoginCommand        string        `json:"loginCommand,omitempty"`
	ConsoleURL          string        `json:"consoleURL,omitempty"`
	Note                string        `json:"note"`
	Usage               ProviderUsage `json:"usage"`
}
type ProviderUsage struct {
	Status    string                `json:"status"`
	Message   string                `json:"message"`
	Windows   []ProviderUsageWindow `json:"windows"`
	CheckedAt *Time                 `json:"checkedAt,omitempty"`
}
type ProviderUsageWindow struct {
	Name        string   `json:"name"`
	Used        *float64 `json:"used,omitempty"`
	Limit       *float64 `json:"limit,omitempty"`
	Remaining   *float64 `json:"remaining,omitempty"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	ResetsAt    *Time    `json:"resetsAt,omitempty"`
}
type ProviderCredentialRequest struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

package wire

// SigninResolveRequest names the page the extension's sign-in widget stands
// on. The vault answers for exactly that page's host.
type SigninResolveRequest struct {
	URL string `json:"url"`
}

// SigninCredential is the one place a stored password leaves the vault: the
// credential the sign-in widget may type on the page it asked about. Source
// names where it came from (a site entry or the shared default), never the
// secret.
type SigninCredential struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Source   string `json:"source"`
}

// OneTimeCode is the newest verification code mailed after the asked-for
// instant. It is returned in the body only and never in a URL or a log.
type OneTimeCode struct {
	Code       string `json:"code"`
	ReceivedAt Time   `json:"receivedAt"`
	From       string `json:"from"`
}

// RiskView is what the Applications section of Settings shows about detection:
// how often each browser source met a wall over the last Days days, the
// signals recorded in that period (newest first, at most 50) and the cooldowns
// still running. It is read-only; cooldowns come from signals and blocked scans.
type RiskView struct {
	Days      int               `json:"days"`
	Baseline  []RiskBaselineRow `json:"baseline"`
	Signals   []RiskSignal      `json:"signals"`
	Cooldowns []RiskCooldown    `json:"cooldowns"`
}

// RiskBaselineRow is the challenge rate of one browser source: the scans that
// ran, the pages they opened, and the scans stopped by a challenge page or by a
// rate-limit page.
type RiskBaselineRow struct {
	Source      string `json:"source"`
	Runs        int    `json:"runs"`
	Visits      int    `json:"visits"`
	Challenges  int    `json:"challenges"`
	RateLimited int    `json:"rateLimited"`
}

// RiskSignal is one detection signal an apply run recorded for a system. EndsAt
// is absent when the policy gave the signal no cooldown. Host is set for a
// site the generic flow serves, whose signal holds that site only.
type RiskSignal struct {
	System string `json:"system"`
	Host   string `json:"host,omitempty"`
	Signal string `json:"signal"`
	RunID  string `json:"runId,omitempty"`
	At     Time   `json:"at"`
	EndsAt *Time  `json:"endsAt,omitempty"`
}

// RiskCooldown is a system or scan source left alone until Until. Kind is
// "apply" (account creation, from a signal) or "scan" (a blocked scan); Signal
// names the signal that supplies the reason; Host is set as in RiskSignal.
type RiskCooldown struct {
	Kind   string `json:"kind"`
	System string `json:"system"`
	Host   string `json:"host,omitempty"`
	Signal string `json:"signal"`
	Until  Time   `json:"until"`
}

// SecretRegistryRow is one registered kind of secret.
type SecretRegistryRow struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Env         string `json:"env"`
	SetupStep   string `json:"setupStep,omitempty"`
	Required    bool   `json:"required"`
}

// SecretEntry is one stored secret as Settings shows it: what exists and where
// it came from, never its value. Problem is the vault's reason when the
// keychain could not be read, which is not the same as absent.
type SecretEntry struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Label        string `json:"label,omitempty"`
	Email        string `json:"email,omitempty"`
	Host         string `json:"host,omitempty"`
	System       string `json:"system,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
	Position     int    `json:"position,omitempty"`
	Present      bool   `json:"present"`
	Source       string `json:"source,omitempty"`
	Problem      string `json:"problem,omitempty"`
	UpdatedAt    *Time  `json:"updatedAt,omitempty"`
	LastSigninAt *Time  `json:"lastSigninAt,omitempty"`
	LastOutcome  string `json:"lastOutcome,omitempty"`
}

// SecretList is the registry and the stored entries.
type SecretList struct {
	Registry []SecretRegistryRow `json:"registry"`
	Entries  []SecretEntry       `json:"entries"`
}

// SecretWrite sets one secret. Value may be empty only when an email is given
// for the shared default. Email and Label are optional.
type SecretWrite struct {
	Value string `json:"value"`
	Email string `json:"email,omitempty"`
	Label string `json:"label,omitempty"`
}

// SecretStatus answers a write or a delete without the value. ShadowedBy names
// the environment variable that now overrides what was stored.
type SecretStatus struct {
	Name       string `json:"name"`
	Present    bool   `json:"present"`
	Source     string `json:"source,omitempty"`
	ShadowedBy string `json:"shadowedBy,omitempty"`
}

// AlternateWrite adds an alternate shared password.
type AlternateWrite struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// AlternateAdded names the alternate that was added.
type AlternateAdded struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AlternateOrder is the new try order: every alternate id exactly once.
type AlternateOrder struct {
	IDs []string `json:"ids"`
}

// SecretTest is the outcome of testing one secret: whether it can be read and
// from where. Live is `unsupported` for a kind without a read-only check of its
// own, which includes every sign-in password. The captcha solver's key is
// checked by reading the account balance: Live is `ok` with BalanceCents and BelowFloor
// (under the solver's floor), `rejected` when the service does not know the
// key, or `failed` with Detail.
type SecretTest struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Source  string `json:"source,omitempty"`
	Live    string `json:"live"`
	// BalanceCents is the balance in whole US cents, rounded down.
	BalanceCents *SafeInt `json:"balanceCents,omitempty"`
	BelowFloor   bool     `json:"belowFloor,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

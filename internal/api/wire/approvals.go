package wire

// ConfirmationCard is a held call waiting for the user. Args is the argument
// object after redaction. Scopes lists the lifetimes the card offers: only
// "once" for a call that no standing approval may cover.
type ConfirmationCard struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionId"`
	TurnID    string   `json:"turnId"`
	Channel   string   `json:"channel"`
	Tool      string   `json:"tool"`
	Summary   string   `json:"summary"`
	Company   string   `json:"company,omitempty"`
	Role      string   `json:"role,omitempty"`
	Host      string   `json:"host,omitempty"`
	Tier      SafeInt  `json:"tier"`
	Code      string   `json:"code"`
	Reason    string   `json:"reason"`
	Args      string   `json:"args"`
	Purpose   string   `json:"purpose"`
	Scopes    []string `json:"scopes"`
	State     string   `json:"state"`
	CreatedAt Time     `json:"createdAt"`
	ExpiresAt Time     `json:"expiresAt"`
	DecidedAt *Time    `json:"decidedAt,omitempty"`
}

// ConfirmationList lists the cards still waiting, oldest first.
type ConfirmationList struct {
	Cards []ConfirmationCard `json:"cards"`
}

// ApproveStanding approves a card for longer than once: scope is
// "conversation" or "today".
type ApproveStanding struct {
	Scope string `json:"scope"`
}

// StandingApproval binds one tool to one target until it ends.
type StandingApproval struct {
	ID         string  `json:"id"`
	Tool       string  `json:"tool"`
	Target     string  `json:"target"`
	Scope      string  `json:"scope"`
	SessionID  string  `json:"sessionId,omitempty"`
	CreatedAt  Time    `json:"createdAt"`
	ExpiresAt  Time    `json:"expiresAt"`
	Uses       SafeInt `json:"uses"`
	LastUsedAt *Time   `json:"lastUsedAt,omitempty"`
}

// StandingApprovalList lists the approvals that have not ended.
type StandingApprovalList struct {
	Approvals []StandingApproval `json:"approvals"`
}

// StandingApprovalRevoked answers a revocation.
type StandingApprovalRevoked struct {
	ID string `json:"id"`
}

// ConfirmationOutcome answers an approval or a decline. Result is what the
// held call produced, or why it did not run; Approval is set when a standing
// approval was created.
type ConfirmationOutcome struct {
	Card     ConfirmationCard  `json:"card"`
	Approval *StandingApproval `json:"approval,omitempty"`
	Result   string            `json:"result,omitempty"`
}

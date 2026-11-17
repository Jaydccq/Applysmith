package apply

import (
	"applysmith/internal/captcha"
	"applysmith/internal/credentials"
	"applysmith/internal/documents"
	"applysmith/internal/gmail"
)

// The production collaborators satisfy the chain's seams: this file fails to
// compile when one of them drifts.
var (
	_ Codes          = (*gmail.Lookups)(nil)
	_ Mail           = (*gmail.Lookups)(nil)
	_ Receipts       = (*gmail.Lookups)(nil)
	_ Documents      = (*documents.Service)(nil)
	_ captcha.Solver = (*captcha.Session)(nil)
	_ Credentials    = (*credentials.Service)(nil)
)

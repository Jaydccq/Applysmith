// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "strings"

// The demo workspace gate. A workspace that carries the demo marker holds the
// fictional example persona: it never submits an application, opens an account
// or reaches a mailbox or a chat, and the persona's own documents may be
// uploaded to a page. The marker is read by the caller from the workspace and
// passed in as Demo; every gate that guards an outbound action checks it
// before any other condition, so no request, setting or instruction can lift
// it.
//
// Two narrow mail openings exist, both decided here. Connecting the owner's
// Gmail account (DemoMailConnect) reads no mail. Reading a site's emailed
// verification code (DemoMailCode) is allowed only when the persona's address
// is a plus-address alias of the connected account, in a dry run, at a code
// dialog of an apply walk, bounded to the ATS's sender domain and to mail
// that arrived after the dialog opened.
const (
	GateDemoWorkspace Gate = "demo_workspace"

	CodeDemoWorkspace = "demo_workspace"
)

// DemoAction is an outbound action that has no gate of its own.
type DemoAction string

const (
	// DemoMail is any Gmail request: a sign-in to read mail, or a read.
	DemoMail DemoAction = "mail"
	// DemoMailConnect is a request of the connection commands (sign-in, the
	// folder list, a message-id count): none of them reads a message.
	DemoMailConnect DemoAction = "mail_connect"
	// DemoMailCode is the bounded lookup of one emailed verification code;
	// DemoInput.Code holds its facts.
	DemoMailCode DemoAction = "mail_code"
	// DemoTelegram is any Bot API request.
	DemoTelegram DemoAction = "telegram"
	// DemoUpload is a document handed to a page for upload; Host is the
	// page's host.
	DemoUpload DemoAction = "upload"
)

// DemoInput holds the facts of the demo gate.
type DemoInput struct {
	Demo   bool
	Action DemoAction
	Host   string
	// Code holds the facts of a DemoMailCode decision.
	Code DemoCodeFacts
}

// DemoCodeFacts are the conditions of the code lookup, every one required.
type DemoCodeFacts struct {
	// DryRun is true when the run does not submit; InWalk when the request
	// comes from an apply run's page walk; AtCodeDialog when that walk stands
	// at a recognised emailed-code dialog.
	DryRun, InWalk, AtCodeDialog bool
	// PersonaEmail is the address the persona applies with (from the profile);
	// PageAddress the address the dialog names, possibly masked, or empty;
	// Account the connected Gmail account, or empty when none is connected.
	PersonaEmail, PageAddress, Account string
	// SenderDomain is the bare domain of the ATS's sender the lookup is
	// bounded to.
	SenderDomain string
	// OpenedMS is when the dialog opened (or its Send / Resend was pressed) and
	// SinceMS where the lookup starts, both in Unix milliseconds, 0 when
	// unknown. The lookup may start before the dialog opened only by the one
	// second mail timestamps are rounded by.
	OpenedMS, SinceMS int64
	// CodesRead counts the codes this dialog already took; Resent is true once
	// its own Resend was pressed. One code per dialog, one more after Resend.
	CodesRead int
	Resent    bool
}

// DemoWorkspace decides an outbound action in a demo workspace: an upload of
// the fictional persona's documents is allowed to any page, so a live dry run
// fills the résumé box; connecting Gmail and one bounded verification-code
// lookup are allowed; every other mail action and Telegram are refused.
func DemoWorkspace(in DemoInput) Decision {
	const g = GateDemoWorkspace
	switch {
	case !in.Demo:
		return decide(g, Allow, CodeOK, "not a demo workspace")
	case in.Action == DemoUpload:
		return decide(g, Allow, CodeOK, "demo workspace: the fictional persona's document uploaded to %s", hostOrUnknown(in.Host))
	case in.Action == DemoMailConnect:
		return decide(g, Allow, CodeOK, "demo workspace: connecting the Gmail account reads no mail")
	case in.Action == DemoMailCode:
		return demoMailCode(in.Code)
	}
	if in.Action == DemoMail {
		return demoDeny(g, "reading or sending mail")
	}
	return demoDeny(g, "a Telegram request")
}

// demoMailCode decides the one mail read a demo may make. The reasons carry no
// address: they are audited.
func demoMailCode(f DemoCodeFacts) Decision {
	const g = GateDemoWorkspace
	deny := func(why string) Decision {
		return decide(g, Deny, CodeDemoWorkspace, "demo workspace: reading an emailed verification code is refused: %s", why)
	}
	switch {
	case !f.InWalk:
		return deny("the request is not part of an apply run's page walk")
	case !f.DryRun:
		return deny("the run is not a dry run")
	case !f.AtCodeDialog:
		return deny("the walk is not at an emailed-code dialog")
	case strings.TrimSpace(f.Account) == "":
		return deny("no Gmail account is connected")
	case !IsPlusAlias(f.PersonaEmail):
		return deny("the persona's address is not a plus-address alias")
	case !AliasOf(f.PersonaEmail, f.Account):
		return deny("the persona's address is not an alias of the connected Gmail account")
	case f.PageAddress != "" && !addressMatches(f.PersonaEmail, f.PageAddress):
		return deny("the dialog names an address other than the persona's")
	case !bareDomain(f.SenderDomain):
		return deny("the ATS's sender domain is not known, so the lookup cannot be bounded")
	case f.OpenedMS <= 0 || f.SinceMS <= 0 || f.SinceMS < f.OpenedMS-1000:
		return deny("the lookup would reach mail from before the dialog opened")
	case f.CodesRead > 1 || f.CodesRead == 1 && !f.Resent:
		return deny("the dialog already took its code")
	}
	return decide(g, Allow, CodeOK, "demo workspace: one verification code from %s received after the dialog opened, for the persona's plus alias of the connected Gmail account", f.SenderDomain)
}

// IsPlusAlias reports whether addr has the shape local+tag@domain with a
// non-empty local part, tag and domain.
func IsPlusAlias(addr string) bool {
	_, ok := aliasBase(addr)
	return ok
}

// AliasOf reports whether alias is a plus-address alias of the mailbox
// account: the address without its +tag equals the account, compared without
// regard to case and, for gmail.com, without the dots of the local part.
func AliasOf(alias, account string) bool {
	base, ok := aliasBase(alias)
	if !ok {
		return false
	}
	acc, ok := splitAddress(account)
	if !ok || strings.Contains(acc.local, "+") {
		return false
	}
	return base.key() == acc.key()
}

type address struct{ local, domain string }

func splitAddress(a string) (address, bool) {
	a = strings.ToLower(strings.TrimSpace(a))
	local, domain, ok := strings.Cut(a, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(a, " \t<>,;") || strings.Contains(domain, "@") {
		return address{}, false
	}
	return address{local, domain}, true
}

// aliasBase is the address without its +tag.
func aliasBase(a string) (address, bool) {
	ad, ok := splitAddress(a)
	if !ok {
		return address{}, false
	}
	base, tag, found := strings.Cut(ad.local, "+")
	if !found || base == "" || tag == "" {
		return address{}, false
	}
	return address{base, ad.domain}, true
}

// key is the mailbox an address names: Gmail ignores the dots of the local
// part and treats googlemail.com as gmail.com.
func (a address) key() string {
	if a.domain == "gmail.com" || a.domain == "googlemail.com" {
		return strings.ReplaceAll(a.local, ".", "") + "@gmail.com"
	}
	return a.local + "@" + a.domain
}

// addressMatches compares the persona's address with the one a dialog names,
// which a site often masks as a***@domain.
func addressMatches(persona, page string) bool {
	p, ok := splitAddress(persona)
	if !ok {
		return false
	}
	pg := strings.ToLower(strings.TrimSpace(page))
	local, domain, ok := strings.Cut(pg, "@")
	if !ok || domain != p.domain {
		return false
	}
	if strings.Contains(local, "*") {
		return []rune(local)[0] == []rune(p.local)[0]
	}
	return local == p.local
}

func bareDomain(d string) bool {
	d = strings.TrimSpace(d)
	return strings.Contains(d, ".") && !strings.ContainsAny(d, " /@:*?") && d == strings.ToLower(d)
}

// demoDeny is the refusal every gate returns first in a demo workspace.
func demoDeny(g Gate, what string) Decision {
	return decide(g, Deny, CodeDemoWorkspace, "demo workspace: %s never happens with the example persona", what)
}

func hostOrUnknown(h string) string {
	if strings.TrimSpace(h) == "" {
		return "an unknown host"
	}
	return h
}

package gmail

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

// A demo workspace reads no mail. Two scopes carried by a context lift that
// for exactly what they name: the connection commands (sign in, find the
// folders, count the ids of a date search) and one bounded verification-code
// lookup that the demo gate allowed. Nothing else carries either scope.
type demoScopeKey struct{}

type demoScope struct {
	connect bool
	code    *codeGrant
}

// codeGrant is the demo gate's allowance for one code lookup: the sender
// domain and the instant the lookup starts, and the messages its own search
// answered (only those may be fetched).
type codeGrant struct {
	domain string
	since  time.Time
	mu     sync.Mutex
	uids   map[string]bool // folder and UID
}

// withDemoConnect marks the connection commands. Only connecting and the
// connection test call it.
func withDemoConnect(ctx context.Context) context.Context {
	return context.WithValue(ctx, demoScopeKey{}, &demoScope{connect: true})
}

// DemoCodeGrant carries the demo gate's allowance of one verification-code
// lookup, bounded to a sender domain and to mail after since. A decision that
// is not an allow from the demo gate yields a context that grants nothing.
func DemoCodeGrant(ctx context.Context, d gates.Decision, domain string, since time.Time) context.Context {
	if d.Gate != gates.GateDemoWorkspace || !d.Allowed() || domain == "" || since.IsZero() {
		return ctx
	}
	g := &codeGrant{domain: strings.ToLower(domain), since: since, uids: map[string]bool{}}
	return context.WithValue(ctx, demoScopeKey{}, &demoScope{code: g})
}

// CodeGrantFrom reports the sender domain and start of the code grant a
// context carries; a caller (a test of the wiring) can see what a lookup is
// bounded to.
func CodeGrantFrom(ctx context.Context) (domain string, since time.Time, ok bool) {
	if sc, _ := ctx.Value(demoScopeKey{}).(*demoScope); sc != nil && sc.code != nil {
		return sc.code.domain, sc.code.since, true
	}
	return "", time.Time{}, false
}

// demoGate is the demo gate at the door of a session. Outside a demo it
// returns no scope and no limit. In a demo a context without a scope is
// refused and recorded; the connection commands are recorded as allowed; a
// code grant was recorded by the run that took it.
func (c *Client) demoGate(ctx context.Context) (*demoScope, error) {
	if !c.st.Demo() {
		return nil, nil
	}
	sc, _ := ctx.Value(demoScopeKey{}).(*demoScope)
	switch {
	case sc != nil && sc.connect:
		return sc, c.st.GuardDemo(ctx, gates.DemoMailConnect, IMAPHost)
	case sc != nil && sc.code != nil:
		return sc, nil
	}
	return nil, c.st.GuardDemo(ctx, gates.DemoMail, IMAPHost)
}

// refuse records and returns the demo gate's refusal of a read outside the
// scope a session holds.
func (c *Client) refuse(ctx context.Context) error {
	return c.st.GuardDemo(ctx, gates.DemoMail, IMAPHost)
}

var connectQueryRE = lazyregexp.New(`^after:\d+$`)

// allowSearch reports whether a search is within the scope: a date search
// for the connection test, or exactly the granted code lookup.
func (sc *demoScope) allowSearch(q string) bool {
	switch {
	case sc == nil:
		return true
	case sc.connect:
		return connectQueryRE.MatchString(q)
	}
	return sc.code.queryOK(q)
}

// allowCursor reports whether the session may list by UID: never in a demo.
func (sc *demoScope) allowCursor() bool { return sc == nil }

// allowFetch reports whether every UID was answered by the granted search.
func (sc *demoScope) allowFetch(folder string, uids []uint32) bool {
	if sc == nil {
		return true
	}
	if sc.code == nil {
		return false
	}
	sc.code.mu.Lock()
	defer sc.code.mu.Unlock()
	return !slices.ContainsFunc(uids, func(u uint32) bool { return !sc.code.uids[uidKey(folder, u)] })
}

// learn keeps, under a code grant, the newest otpMaxMessages UIDs of the
// granted search, the only ones that may then be fetched.
func (sc *demoScope) learn(folder string, uids []uint32) []uint32 {
	if sc == nil || sc.code == nil {
		return uids
	}
	uids = uids[max(len(uids)-otpMaxMessages, 0):]
	sc.code.mu.Lock()
	defer sc.code.mu.Unlock()
	for _, u := range uids {
		sc.code.uids[uidKey(folder, u)] = true
	}
	return uids
}

func uidKey(folder string, uid uint32) string {
	return folder + "\x00" + strconv.FormatUint(uint64(uid), 10)
}

// queryOK reports whether a search is exactly the bounded code lookup: after
// the grant's start, in any folder, from its sender domain, with the code
// vocabulary.
func (g *codeGrant) queryOK(q string) bool {
	rest, ok := strings.CutPrefix(q, "after:")
	if !ok {
		return false
	}
	secs, tail, ok := strings.Cut(rest, " ")
	if n, err := strconv.ParseInt(secs, 10, 64); !ok || err != nil || n < g.since.Unix() {
		return false
	}
	return tail == "in:anywhere from:"+g.domain+" "+extract.OTPQueryTerms()
}

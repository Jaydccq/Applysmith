package gmail

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-imap/responses"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// IMAPHost is the one server the client reaches, on port 993 over TLS.
const IMAPHost = "imap.gmail.com"

// RequestTimeout bounds every IMAP command and the connection itself; in a
// FETCH it bounds each message.
const RequestTimeout = 30 * time.Second

const (
	// maxMessageBytes bounds what is read of one message: its first 4 MiB,
	// where the text parts come before any attachment.
	maxMessageBytes = 4 << 20
	// bodyChunk and idChunk bound the messages one FETCH asks for.
	bodyChunk = 50
	idChunk   = 500
)

// Credentials are a mailbox address and its Google app password.
type Credentials struct{ Address, Password string }

// Client reads the mailbox over IMAP and never changes it: folders are opened
// with EXAMINE, bodies are fetched with BODY.PEEK so nothing is marked read,
// and the session sends no command outside readCommands.
type Client struct {
	Account *Account
	// Dial opens the TLS connection; tests point it at a local server.
	Dial func(ctx context.Context) (net.Conn, error)
	st   *store.Store
	// timeout bounds a command in place of RequestTimeout (tests).
	timeout time.Duration
}

// NewClient builds the client of a workspace: st carries the demo gate.
func NewClient(account *Account, st *store.Store) *Client {
	return &Client{Account: account, Dial: dialGmail, st: st}
}

func dialGmail(ctx context.Context) (net.Conn, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: RequestTimeout}, Config: &tls.Config{ServerName: IMAPHost, MinVersion: tls.VersionTLS12}}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(IMAPHost, "993"))
}

// readCommands are the only commands a session sends. IDLE only waits for
// Gmail to announce changes to the examined folder.
var readCommands = []string{"LOGIN", "LIST", "EXAMINE", "UID SEARCH", "UID FETCH", "IDLE", "LOGOUT"}

// folder is an examined folder and its UID state.
type folder struct {
	name     string
	validity uint32
	next     uint32
}

// session is one logged-in connection with All Mail examined.
type session struct {
	c     *client.Client
	conn  net.Conn
	scope *demoScope
	// refuse records and returns the demo gate's refusal of a read the
	// scope does not cover.
	refuse func() error
	stop   func() bool
	// all and junk name the \All and \Junk folders; their names are
	// localized. Junk is empty when the account shows no spam folder.
	all, junk string
	cur       folder
}

// open logs in, finds the folders and examines All Mail. With nil creds the
// stored credentials are used, and a refusal of them is recorded on the
// account.
func (c *Client) open(ctx context.Context, creds *Credentials) (*session, error) {
	scope, err := c.demoGate(ctx)
	if err != nil {
		return nil, err
	}
	stored := creds == nil
	if stored {
		cr, err := c.Account.credentials(ctx)
		if err != nil {
			return nil, err
		}
		creds = &cr
	}
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, unreachable(err)
	}
	_ = conn.SetDeadline(time.Now().Add(RequestTimeout))
	ic, err := client.New(conn)
	if err != nil {
		_ = conn.Close()
		return nil, unreachable(err)
	}
	ic.ErrorLog = log.New(io.Discard, "", 0)
	ic.Timeout = cmp.Or(c.timeout, RequestTimeout)
	s := &session{c: ic, conn: conn, scope: scope, refuse: func() error { return c.refuse(ctx) }}
	s.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := s.exec(&commands.Login{Username: creds.Address, Password: creds.Password}, nil); err != nil {
		s.close()
		var le *LoginError
		if stored && errors.As(err, &le) {
			c.Account.markRefused(ctx, err)
		}
		return nil, ctxErr(ctx, err)
	}
	if stored {
		c.Account.markAccepted(ctx)
	}
	if err := s.findFolders(); err != nil {
		s.close()
		return nil, ctxErr(ctx, err)
	}
	if _, err := s.examine(s.all); err != nil {
		s.close()
		return nil, ctxErr(ctx, err)
	}
	return s, nil
}

// ctxErr prefers the context's end over the closed connection it caused.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// close logs out and closes the connection. The context's hook is taken off
// only after the logout, so a cancelled context also ends a logout that
// nobody answers.
func (s *session) close() {
	_ = s.c.Logout()
	s.stop()
	_ = s.conn.Close()
}

// exec sends one command. A transport failure is unreachable; a NO or BAD
// answer is a commandError, or a LoginError for LOGIN.
func (s *session) exec(cmd imap.Commander, h responses.Handler) error {
	c := cmd.Command()
	name := c.Name
	if name == "UID" && len(c.Arguments) > 0 {
		if sub, ok := c.Arguments[0].(imap.RawString); ok {
			name += " " + string(sub)
		}
	}
	if !slices.Contains(readCommands, name) {
		return fmt.Errorf("%w: %s", ErrRefused, name)
	}
	st, err := s.c.Execute(cmd, h)
	if err != nil {
		return unreachable(err)
	}
	if st.Type == imap.StatusRespOk {
		return nil
	}
	if name == "LOGIN" {
		return loginFailure(st)
	}
	return &commandError{Command: name, Code: string(st.Code), Info: st.Info}
}

func (s *session) findFolders() error {
	var infos []*imap.MailboxInfo
	h := responses.HandlerFunc(func(resp imap.Resp) error {
		name, fields, ok := imap.ParseNamedResp(resp)
		if !ok || name != "LIST" {
			return responses.ErrUnhandled
		}
		mb := &imap.MailboxInfo{}
		if err := mb.Parse(fields); err != nil {
			return err
		}
		infos = append(infos, mb)
		return nil
	})
	if err := s.exec(&commands.List{Reference: "", Mailbox: "*"}, h); err != nil {
		return err
	}
	for _, mb := range infos {
		switch {
		case slices.Contains(mb.Attributes, imap.AllAttr):
			s.all = mb.Name
		case slices.Contains(mb.Attributes, imap.JunkAttr):
			s.junk = mb.Name
		}
	}
	if s.all == "" {
		return ErrNoAllMail
	}
	return nil
}

// examine opens a folder read-only.
func (s *session) examine(name string) (folder, error) {
	h := &responses.Select{Mailbox: &imap.MailboxStatus{Items: map[imap.StatusItem]any{}}}
	if err := s.exec(&commands.Select{Mailbox: name, ReadOnly: true}, h); err != nil {
		return folder{}, err
	}
	s.cur = folder{name: name, validity: h.Mailbox.UidValidity, next: h.Mailbox.UidNext}
	return s.cur, nil
}

// idleEnd says why an IDLE ended.
type idleEnd int

const (
	idleHeld    idleEnd = iota // the hold ran out
	idleArrived                // Gmail announced new mail
	idleWoken                  // the caller asked to stop
	idleSlept                  // the computer slept; the connection was closed
)

// sleepCheck is how often an IDLE compares the wall clock with the monotonic
// one. Timers and read deadlines run on the monotonic clock, which stands
// still while the computer sleeps, so without it a connection Gmail dropped
// during sleep would be noticed only when the hold ran out.
const sleepCheck = 15 * time.Second

// idleExists is the IDLE handler: an EXISTS, which is how Gmail announces new
// mail in the examined folder, ends the wait.
type idleExists struct {
	*responses.Idle
	exists chan struct{}
}

func (h *idleExists) Handle(resp imap.Resp) error {
	if name, _, ok := imap.ParseNamedResp(resp); ok && name == "EXISTS" {
		select {
		case h.exists <- struct{}{}:
		default:
		}
		return nil
	}
	return h.Idle.Handle(resp)
}

// idle holds IDLE on the examined folder until Gmail announces new mail, hold
// runs out, wake fires or ctx ends. The session's command timeout must exceed
// hold, since the deadline of the IDLE command bounds the whole wait. Every
// return closes stop, which releases the goroutine go-imap keeps waiting on it.
func (s *session) idle(ctx context.Context, hold time.Duration, wake <-chan struct{}) (idleEnd, error) {
	stop := make(chan struct{})
	h := &idleExists{Idle: &responses.Idle{Stop: stop, RepliesCh: make(chan []byte, 10)}, exists: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- s.exec(&commands.Idle{}, h) }()
	timer := time.NewTimer(hold)
	defer timer.Stop()
	check := time.NewTicker(sleepCheck)
	defer check.Stop()
	wall, mono := time.Now().Round(0), time.Now()
	end := idleHeld
wait:
	for {
		select {
		case <-h.exists:
			end = idleArrived
			break wait
		case <-timer.C:
			break wait
		case <-wake:
			end = idleWoken
			break wait
		case <-ctx.Done():
			break wait
		case err := <-done:
			// Refused, or the connection broke, before DONE was sent.
			close(stop)
			return end, ctxErr(ctx, err)
		case <-check.C:
			if time.Now().Round(0).Sub(wall)-time.Since(mono) > sleepCheck {
				// DONE would wait on a dead connection until its deadline.
				_ = s.conn.Close()
				<-done
				close(stop)
				return idleSlept, nil
			}
		}
	}
	close(stop)
	return end, ctxErr(ctx, <-done)
}

// uidSearch is UID SEARCH with raw criteria.
type uidSearch []any

func (c uidSearch) Command() *imap.Command {
	return &imap.Command{Name: "UID", Arguments: append([]any{imap.RawString("SEARCH")}, c...)}
}

func (s *session) search(criteria ...any) ([]uint32, error) {
	h := &responses.Search{}
	if err := s.exec(uidSearch(criteria), h); err != nil {
		return nil, err
	}
	slices.Sort(h.Ids)
	return h.Ids, nil
}

// searchRaw runs a search in Gmail's own syntax in the examined folder and
// returns the UIDs, oldest first.
func (s *session) searchRaw(q string) ([]uint32, error) {
	if !s.scope.allowSearch(q) {
		return nil, s.refuse()
	}
	uids, err := s.search(imap.RawString("X-GM-RAW"), q)
	if err != nil {
		return nil, err
	}
	return s.scope.learn(s.cur.name, uids), nil
}

// searchAfter returns the UIDs above uid, oldest first.
func (s *session) searchAfter(uid uint32) ([]uint32, error) {
	if !s.scope.allowCursor() {
		return nil, s.refuse()
	}
	uids, err := s.search(imap.RawString("UID"), imap.RawString(strconv.FormatUint(uint64(uid)+1, 10)+":*"))
	if err != nil {
		return nil, err
	}
	// "n:*" always includes the highest UID, even below n.
	return slices.DeleteFunc(uids, func(u uint32) bool { return u <= uid }), nil
}

// fetch runs UID FETCH over uids, chunk at a time, and hands each answer to
// each as it is read, so no more than one raw message is held at once. Each
// answer moves the command's deadline on, so the timeout bounds one message
// and not a chunk of them.
func (s *session) fetch(uids []uint32, items []imap.FetchItem, chunk int, each func(*imap.Message)) error {
	if !s.scope.allowFetch(s.cur.name, uids) {
		return s.refuse()
	}
	for start := 0; start < len(uids); start += chunk {
		set := new(imap.SeqSet)
		set.AddNum(uids[start:min(start+chunk, len(uids))]...)
		h := responses.HandlerFunc(func(resp imap.Resp) error {
			name, fields, ok := imap.ParseNamedResp(resp)
			if !ok || name != "FETCH" || len(fields) < 2 {
				return responses.ErrUnhandled
			}
			list, _ := fields[1].([]any)
			msg := &imap.Message{}
			if err := msg.Parse(list); err != nil {
				return err
			}
			if msg.Uid == 0 || !set.Contains(msg.Uid) {
				return responses.ErrUnhandled
			}
			each(msg)
			_ = s.conn.SetDeadline(time.Now().Add(s.c.Timeout))
			return nil
		})
		if err := s.exec(&commands.Uid{Cmd: &commands.Fetch{SeqSet: set, Items: items}}, h); err != nil {
			return err
		}
	}
	return nil
}

var idItems = []imap.FetchItem{imap.FetchUid, "X-GM-MSGID"}

// ids maps UIDs to Gmail message ids. A UID with no answer was expunged.
func (s *session) ids(uids []uint32) (map[uint32]string, error) {
	out := make(map[uint32]string, len(uids))
	err := s.fetch(uids, idItems, idChunk, func(m *imap.Message) {
		if id := gmID(m.Items["X-GM-MSGID"]); id != "" {
			out[m.Uid] = id
		}
	})
	return out, err
}

// bodySection is the first maxMessageBytes of the whole message, read
// without setting \Seen.
var bodySection = imap.FetchItem("BODY.PEEK[]<0." + strconv.Itoa(maxMessageBytes) + ">")

var messageItems = []imap.FetchItem{imap.FetchUid, "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS", imap.FetchInternalDate, bodySection}

// messages fetches and parses whole messages. A UID with no answer was
// expunged and is left out.
func (s *session) messages(uids []uint32) (map[uint32]*Message, error) {
	out := make(map[uint32]*Message, len(uids))
	err := s.fetch(uids, messageItems, bodyChunk, func(im *imap.Message) {
		m := &Message{ID: gmID(im.Items["X-GM-MSGID"]), ThreadID: gmID(im.Items["X-GM-THRID"]), InternalMS: im.InternalDate.UnixMilli(), Labels: gmLabels(im.Items["X-GM-LABELS"])}
		if m.ID == "" {
			return
		}
		if m.ThreadID == "" {
			m.ThreadID = m.ID
		}
		for _, lit := range im.Body {
			if raw, err := io.ReadAll(lit); err == nil {
				m.parse(raw)
			}
		}
		out[im.Uid] = m
	})
	return out, err
}

// gmID renders X-GM-MSGID or X-GM-THRID as Gmail's hexadecimal id.
func gmID(v any) string {
	s, err := imap.ParseString(v)
	if err != nil {
		return ""
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n, 16)
}

// systemLabels maps Gmail's IMAP system labels to its label ids.
var systemLabels = map[string]string{
	`\Inbox`: "INBOX", `\Sent`: "SENT", `\Important`: "IMPORTANT", `\Starred`: "STARRED",
	`\Draft`: "DRAFT", `\Spam`: "SPAM", `\Trash`: "TRASH",
}

func gmLabels(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, f := range list {
		s, err := imap.ParseString(f)
		if err != nil || s == "" {
			continue
		}
		if id, ok := systemLabels[s]; ok {
			s = id
		}
		out = append(out, s)
	}
	return out
}

// Find runs one search in All Mail and then in Spam, which All Mail leaves
// out, and returns up to limit messages, newest first.
func (c *Client) Find(ctx context.Context, q string, limit int) ([]*Message, error) {
	if sc, _ := ctx.Value(demoScopeKey{}).(*demoScope); c.st.Demo() && sc != nil && !sc.allowSearch(q) {
		return nil, c.refuse(ctx)
	}
	s, err := c.open(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var out []*Message
	for _, name := range []string{s.all, s.junk} {
		if name == "" {
			continue
		}
		if name != s.cur.name {
			if _, err := s.examine(name); err != nil {
				return nil, ctxErr(ctx, err)
			}
		}
		uids, err := s.searchRaw(q)
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		msgs, err := s.messages(uids[max(len(uids)-limit, 0):])
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		for _, m := range msgs {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *Message) int {
		return cmp.Or(cmp.Compare(b.InternalMS, a.InternalMS), strings.Compare(b.ID, a.ID))
	})
	return out[:min(len(out), limit)], nil
}

// LoginError is Gmail refusing the address and app password. Its text is
// Gmail's own and never carries the password.
type LoginError struct{ Detail string }

func (e *LoginError) Error() string {
	return "Gmail refused the sign-in (" + e.Detail + "): " + LoginFix
}

// ConnectRefusal is what a refused connection attempt says: the address and
// password just typed, not stored ones, were wrong.
func (e *LoginError) ConnectRefusal() string {
	return "Gmail refused the sign-in (" + e.Detail + "): check the address, and enter an app password from your Google Account (Security, App passwords), not the account's own password"
}

// LoginFix is what fixes a refused sign-in.
const LoginFix = "create a new app password in your Google Account (Security, App passwords) and enter it with `" + ConnectCommand + "` or in Settings, Gmail"

// transientLogin is Gmail's wording for a refusal that passes by itself.
var transientLogin = lazyregexp.New(`(?i)too many|simultaneous|try again|temporar|rate`)

func loginFailure(st *imap.StatusResp) error {
	if transientLogin.MatchString(st.Info) {
		return &commandError{Command: "LOGIN", Code: string(st.Code), Info: st.Info}
	}
	return &LoginError{Detail: strings.TrimSpace(st.Info)}
}

// commandError is a NO or BAD answer to a read.
type commandError struct{ Command, Code, Info string }

func (e *commandError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("gmail %s: [%s] %s", e.Command, e.Code, e.Info)
	}
	return fmt.Sprintf("gmail %s: %s", e.Command, e.Info)
}

// UnreachableError is a connection that failed or broke.
type UnreachableError struct{ Err error }

func (e *UnreachableError) Error() string { return "Gmail could not be reached: " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

func unreachable(err error) error {
	var ue *UnreachableError
	if errors.As(err, &ue) {
		return err
	}
	return &UnreachableError{Err: err}
}

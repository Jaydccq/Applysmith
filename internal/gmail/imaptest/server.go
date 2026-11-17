// Package imaptest is a scripted Gmail IMAP server on a loopback port for
// the tests of the packages that read Gmail: an All Mail and a Spam folder
// under localized names, Gmail's X-GM items, searches answered from a table or
// a function, and IDLE, which announces each message a folder gains. It logs
// every command it is sent. No product code imports it.
package imaptest

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"mime"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
)

// The folders, by their index in Add.
const (
	AllMail = 0
	Spam    = 1
)

// Message is one message the server holds. Its ids are hexadecimal, as
// Gmail's are; Thread defaults to ID. Raw, when set, is sent instead of the
// message rendered from the other fields.
type Message struct {
	ID, Thread, From, To, Cc, Subject, Text, HTML string
	MS                                            int64
	Labels                                        []string
	Raw                                           string
}

// Server is the scripted server. Its setters may be called while a client
// is connected.
type Server struct {
	ln net.Listener

	mu       sync.Mutex
	user     string
	pass     string
	refuse   string              // LOGIN answers NO with this text when set
	noAll    bool                // LIST shows no \All folder
	folders  []*folder           // All Mail, then Spam
	searches map[string][]string // X-GM-RAW query -> message ids
	searchFn func(attr, q string) []string
	log      []string // one line per command, without its tag
	bodyHook func(ids []string) bool
	vanish   map[string]bool
	bodyGap  time.Duration // waited before each message of a body FETCH
	noBye    bool          // LOGOUT gets no answer
	// changed is closed, and replaced, whenever a folder gains a message, so
	// an idling connection wakes.
	changed chan struct{}
	conns   map[net.Conn]bool
}

type folder struct {
	name, attr string
	validity   uint32
	next       uint32
	msgs       []*message
}

type message struct {
	uid        uint32
	id, thread string
	labels     []string
	date       time.Time
	raw        string
}

// New starts a server that accepts one address and app password. Close
// stops it.
func New(user, password string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &Server{ln: ln, user: user, pass: password, searches: map[string][]string{}, changed: make(chan struct{}), conns: map[net.Conn]bool{},
		folders: []*folder{{name: "[Gmail]/Alle Nachrichten", attr: `\All`, validity: 7, next: 1}, {name: "[Gmail]/Spam", attr: `\Junk`, validity: 3, next: 1}}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, nil
}

// Close stops accepting connections: Gmail can no longer be reached.
func (f *Server) Close() { _ = f.ln.Close() }

// Dial connects to the server; it is the Dial of a gmail.Client.
func (f *Server) Dial(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.ln.Addr().String())
}

// Add stores a message in a folder (AllMail or Spam) under the next UID and
// returns the UID. An id Gmail could not name (not lower-case hexadecimal, or
// with a leading zero) panics.
func (f *Server) Add(folderIndex int, m Message) uint32 {
	for _, id := range []string{m.ID, m.Thread} {
		if n, err := strconv.ParseUint(id, 16, 64); id != "" && (err != nil || strconv.FormatUint(n, 16) != id) {
			panic(fmt.Sprintf("imaptest: message id %q is not lower-case hexadecimal without a leading zero", id))
		}
	}
	raw := m.Raw
	if raw == "" {
		raw = m.render()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fd := f.folders[folderIndex]
	thread := m.Thread
	if thread == "" {
		thread = m.ID
	}
	fm := &message{uid: fd.next, id: m.ID, thread: thread, labels: m.Labels, date: time.UnixMilli(m.MS).UTC(), raw: raw}
	fd.next++
	fd.msgs = append(fd.msgs, fm)
	close(f.changed)
	f.changed = make(chan struct{})
	return fm.uid
}

// Drop closes every open connection, as a network change does.
func (f *Server) Drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.conns {
		_ = c.Close()
	}
}

// Connections counts the connections open now.
func (f *Server) Connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// Remove expunges a message from every folder.
func (f *Server) Remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fd := range f.folders {
		fd.msgs = slices.DeleteFunc(fd.msgs, func(m *message) bool { return m.id == id })
	}
}

// Commands returns the commands received that start with prefix, one line
// each without the tag; LOGIN is logged without its arguments.
func (f *Server) Commands(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// SetUser changes the address the server accepts.
func (f *Server) SetUser(user string) { f.set(func() { f.user = user }) }

// Refuse makes LOGIN answer NO [AUTHENTICATIONFAILED] with text; an empty
// text accepts the right password again.
func (f *Server) Refuse(text string) { f.set(func() { f.refuse = text }) }

// HideAllMail lists All Mail without its \All attribute.
func (f *Server) HideAllMail() { f.set(func() { f.noAll = true }) }

// Search answers an X-GM-RAW search for q with the messages of ids. A query
// with no answer finds nothing, except after:<seconds>, which finds the
// messages received since.
func (f *Server) Search(q string, ids ...string) { f.set(func() { f.searches[q] = ids }) }

// SearchWith answers every X-GM-RAW search with the ids fn returns for the
// folder's attribute (\All or \Junk) and the query, in place of the table.
func (f *Server) SearchWith(fn func(attr, q string) []string) { f.set(func() { f.searchFn = fn }) }

// OnBodyFetch sees the ids of each body FETCH before it is answered and
// fails it by returning false.
func (f *Server) OnBodyFetch(fn func(ids []string) bool) { f.set(func() { f.bodyHook = fn }) }

// PaceBodies waits d before each message of a body FETCH, as a slow link does.
func (f *Server) PaceBodies(d time.Duration) { f.set(func() { f.bodyGap = d }) }

// IgnoreLogout leaves LOGOUT unanswered, as a connection that died does.
func (f *Server) IgnoreLogout() { f.set(func() { f.noBye = true }) }

// Vanish leaves ids out of every body FETCH, as if they were deleted after
// the search.
func (f *Server) Vanish(ids ...string) {
	f.set(func() {
		f.vanish = map[string]bool{}
		for _, id := range ids {
			f.vanish[id] = true
		}
	})
}

// SetValidity changes a folder's UIDVALIDITY, as Gmail does when it
// renumbers a folder.
func (f *Server) SetValidity(folderIndex int, v uint32) {
	f.set(func() { f.folders[folderIndex].validity = v })
}

func (f *Server) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *Server) serve(conn net.Conn) {
	f.set(func() { f.conns[conn] = true })
	defer f.set(func() { delete(f.conns, conn) })
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := imap.NewReader(bufio.NewReader(conn))
	send := func(lines ...string) {
		for _, l := range lines {
			_, _ = w.WriteString(l + "\r\n")
		}
		_ = w.Flush()
	}
	send("* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1 IDLE] Gimap ready")
	var cur *folder
	for {
		fields, err := r.ReadLine()
		if err != nil {
			return
		}
		cmd := &imap.Command{}
		if cmd.Parse(fields) != nil {
			return
		}
		name, args := cmd.Name, cmd.Arguments
		if name == "UID" && len(args) > 0 {
			sub, _ := imap.ParseString(args[0])
			name, args = name+" "+strings.ToUpper(sub), args[1:]
		}
		line := name
		if name != "LOGIN" {
			for _, a := range args {
				line += " " + fmt.Sprint(a)
			}
		}
		f.mu.Lock()
		f.log = append(f.log, line)
		f.mu.Unlock()
		switch name {
		case "CAPABILITY":
			send("* CAPABILITY IMAP4rev1 X-GM-EXT-1 IDLE", cmd.Tag+" OK")
		case "LOGIN":
			user, _ := imap.ParseString(args[0])
			pass, _ := imap.ParseString(args[1])
			f.mu.Lock()
			refuse, ok := f.refuse, user == f.user && pass == f.pass
			f.mu.Unlock()
			switch {
			case refuse != "":
				send(cmd.Tag + " NO [AUTHENTICATIONFAILED] " + refuse)
			case !ok:
				send(cmd.Tag + " NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)")
			default:
				send(cmd.Tag + " OK " + user + " authenticated (Success)")
			}
		case "LIST":
			send(`* LIST (\HasNoChildren) "/" "INBOX"`)
			f.mu.Lock()
			noAll := f.noAll
			f.mu.Unlock()
			for _, fd := range f.folders {
				attr := fd.attr
				if noAll && attr == `\All` {
					attr = ""
				}
				send(fmt.Sprintf(`* LIST (\HasNoChildren %s) "/" %q`, attr, fd.name))
			}
			send(cmd.Tag + " OK Success")
		case "EXAMINE":
			want, _ := imap.ParseString(args[0])
			cur = nil
			f.mu.Lock()
			for _, fd := range f.folders {
				if fd.name == want {
					cur = fd
					send("* FLAGS (\\Answered \\Flagged \\Draft \\Deleted \\Seen)", fmt.Sprintf("* %d EXISTS", len(fd.msgs)),
						fmt.Sprintf("* OK [UIDVALIDITY %d] UIDs valid.", fd.validity), fmt.Sprintf("* OK [UIDNEXT %d] Predicted next UID.", fd.next))
				}
			}
			f.mu.Unlock()
			if cur == nil {
				send(cmd.Tag + " NO [NONEXISTENT] Unknown Mailbox")
				continue
			}
			send(cmd.Tag + " OK [READ-ONLY] " + want + " selected. (Success)")
		case "UID SEARCH":
			send(f.search(cur, args), cmd.Tag+" OK SEARCH completed (Success)")
		case "UID FETCH":
			if !f.fetch(cur, args, w) {
				send(cmd.Tag + " NO [SERVERBUG] scripted fetch failure")
				continue
			}
			send(cmd.Tag + " OK Success")
		case "IDLE":
			if cur == nil {
				send(cmd.Tag + " BAD No mailbox selected")
				continue
			}
			send("+ idling")
			if !f.idle(cur, r, send) {
				return
			}
			send(cmd.Tag + " OK IDLE terminated (Success)")
		case "LOGOUT":
			f.mu.Lock()
			noBye := f.noBye
			f.mu.Unlock()
			if noBye {
				continue
			}
			send("* BYE LOGOUT Requested", cmd.Tag+" OK 73 good day (Success)")
			return
		default:
			send(cmd.Tag + " BAD Unknown command")
		}
	}
}

// idle answers an IDLE: one EXISTS each time the folder gains messages, until
// the client sends DONE. It reports false when the connection ended.
func (f *Server) idle(cur *folder, r *imap.Reader, send func(...string)) bool {
	f.mu.Lock()
	changed, n := f.changed, len(cur.msgs)
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := r.ReadLine()
		done <- err
	}()
	for {
		select {
		case err := <-done:
			return err == nil
		case <-changed:
			f.mu.Lock()
			changed = f.changed
			m := len(cur.msgs)
			f.mu.Unlock()
			if m != n {
				n = m
				send(fmt.Sprintf("* %d EXISTS", m))
			}
		}
	}
}

func (f *Server) search(cur *folder, args []any) string {
	key, _ := imap.ParseString(args[0])
	val, _ := imap.ParseString(args[1])
	f.mu.Lock()
	fn := f.searchFn
	f.mu.Unlock()
	var fnIDs []string
	if fn != nil && strings.EqualFold(key, "X-GM-RAW") {
		fnIDs = fn(cur.attr, val)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var hits []string
	switch strings.ToUpper(key) {
	case "X-GM-RAW":
		ids, scripted := f.searches[val]
		if fn != nil {
			ids, scripted = fnIDs, true
		}
		after, isDate := strings.CutPrefix(val, "after:")
		secs, _ := strconv.ParseInt(after, 10, 64)
		for _, m := range cur.msgs {
			if scripted && slices.Contains(ids, m.id) || !scripted && isDate && m.date.Unix() >= secs {
				hits = append(hits, strconv.FormatUint(uint64(m.uid), 10))
			}
		}
	case "UID":
		from, _ := strconv.ParseUint(strings.TrimSuffix(val, ":*"), 10, 32)
		for _, m := range cur.msgs {
			if uint64(m.uid) >= from {
				hits = append(hits, strconv.FormatUint(uint64(m.uid), 10))
			}
		}
		// "n:*" holds the highest UID even when it is below n.
		if len(hits) == 0 && len(cur.msgs) > 0 {
			hits = append(hits, strconv.FormatUint(uint64(cur.msgs[len(cur.msgs)-1].uid), 10))
		}
	}
	return strings.TrimSpace("* SEARCH " + strings.Join(hits, " "))
}

func (f *Server) fetch(cur *folder, args []any, w *bufio.Writer) bool {
	setText, _ := imap.ParseString(args[0])
	set, _ := imap.ParseSeqSet(setText)
	items, _ := args[1].([]any)
	var names []string
	for _, it := range items {
		n, _ := imap.ParseString(it)
		names = append(names, n)
	}
	withBody := slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "BODY") })
	f.mu.Lock()
	hook, vanish, gap := f.bodyHook, maps.Clone(f.vanish), f.bodyGap
	var msgs []*message
	seqs := map[*message]int{}
	for i, m := range cur.msgs {
		if set.Contains(m.uid) && !(withBody && vanish[m.id]) {
			msgs = append(msgs, m)
			seqs[m] = i + 1
		}
	}
	f.mu.Unlock()
	if withBody && hook != nil {
		var ids []string
		for _, m := range msgs {
			ids = append(ids, m.id)
		}
		if !hook(ids) {
			return false
		}
	}
	for _, m := range msgs {
		var parts []string
		for _, name := range names {
			switch {
			case name == "UID":
				parts = append(parts, fmt.Sprintf("UID %d", m.uid))
			case name == "X-GM-MSGID", name == "X-GM-THRID":
				hex := m.id
				if name == "X-GM-THRID" {
					hex = m.thread
				}
				n, _ := strconv.ParseUint(hex, 16, 64)
				parts = append(parts, fmt.Sprintf("%s %d", name, n))
			case name == "X-GM-LABELS":
				var ls []string
				for _, l := range m.labels {
					ls = append(ls, strconv.Quote(l))
				}
				parts = append(parts, "X-GM-LABELS ("+strings.Join(ls, " ")+")")
			case name == "INTERNALDATE":
				parts = append(parts, `INTERNALDATE "`+m.date.Format("02-Jan-2006 15:04:05 -0700")+`"`)
			}
		}
		line := fmt.Sprintf("* %d FETCH (%s", seqs[m], strings.Join(parts, " "))
		if withBody {
			body := m.raw
			for _, name := range names {
				if _, cut, ok := strings.Cut(name, "<0."); ok {
					n, _ := strconv.Atoi(strings.TrimSuffix(cut, ">"))
					body = body[:min(n, len(body))]
				}
			}
			line += fmt.Sprintf(" BODY[]<0> {%d}\r\n%s", len(body), body)
		}
		if withBody && gap > 0 {
			_ = w.Flush()
			time.Sleep(gap)
		}
		_, _ = w.WriteString(line + ")\r\n")
	}
	_ = w.Flush()
	return true
}

// render is the message as RFC 822 text: one text/plain part, one
// text/html part, or both under multipart/alternative.
func (m Message) render() string {
	var b strings.Builder
	head := func(k, v string) {
		if v != "" {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	head("From", m.From)
	head("To", m.To)
	head("Cc", m.Cc)
	head("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	head("Date", time.UnixMilli(m.MS).UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	part := func(kind, body string) string {
		return "Content-Type: text/" + kind + "; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n"
	}
	switch {
	case m.Text != "" && m.HTML != "":
		b.WriteString("Content-Type: multipart/alternative; boundary=\"b0\"\r\n\r\n--b0\r\n" + part("plain", m.Text) + "--b0\r\n" + part("html", m.HTML) + "--b0--\r\n")
	case m.HTML != "":
		b.WriteString(part("html", m.HTML))
	default:
		b.WriteString(part("plain", m.Text))
	}
	return b.String()
}

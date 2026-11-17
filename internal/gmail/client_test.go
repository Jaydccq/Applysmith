package gmail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"

	"applysmith/internal/gmail/imaptest"
)

// A message is read with Gmail's ids, label names and receipt time, and its
// encoded headers and parts come out as text.
func TestGmail_Client_ReadsAMessageWithGmailsIDs(t *testing.T) {
	e := newEnv(t)
	e.connect()
	raw := strings.Join([]string{
		"From: =?ISO-8859-1?Q?Ren=E9e_Recruiting?= <" + addr("jobs", "example.com") + ">",
		"To: " + addr("alex", "example.com"),
		"Subject: =?UTF-8?B?VGhhbmsgeW91IGZvciBhcHBseWluZyDinJM=?=",
		"Content-Type: multipart/mixed; boundary=outer",
		"",
		"--outer",
		"Content-Type: multipart/alternative; boundary=inner",
		"",
		"--inner",
		"Content-Type: text/plain; charset=iso-8859-1",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Caf=E9 interview =",
		"on Monday",
		"--inner",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"",
		"PHA+SGVsbG8gPGI+dGhlcmU8L2I+PC9wPjxzdHlsZT54e308L3N0eWxlPg==",
		"--inner--",
		"--outer",
		"Content-Type: text/plain",
		"Content-Disposition: attachment; filename=offer.txt",
		"",
		"attachment text",
		"--outer--",
		"",
	}, "\r\n")
	e.imap.Add(imaptest.AllMail, msgSpec{ID: "18c2f3a4b5d6e7f8", Thread: "18c2f3a4b5d6e000",
		Labels: []string{`\Inbox`, `\Important`, "Job search"}, MS: 1700000000000, Raw: raw})
	e.imap.Search("subject:applying", "18c2f3a4b5d6e7f8")
	msgs, err := e.client.Find(e.ctx, "subject:applying", 5)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("find = %v, %v", msgs, err)
	}
	m := msgs[0]
	if m.ID != "18c2f3a4b5d6e7f8" || m.ThreadID != "18c2f3a4b5d6e000" || m.InternalMS != 1700000000000 {
		t.Fatalf("ids/time = %q %q %d", m.ID, m.ThreadID, m.InternalMS)
	}
	if !slices.Equal(m.Labels, []string{"INBOX", "IMPORTANT", "Job search"}) {
		t.Fatalf("labels = %v", m.Labels)
	}
	if m.Subject != "Thank you for applying ✓" || !strings.HasPrefix(m.From, "Renée Recruiting <") {
		t.Fatalf("headers = %q / %q", m.Subject, m.From)
	}
	if m.Plain != "Café interview on Monday" || !strings.Contains(m.HTML, "<b>there</b>") {
		t.Fatalf("parts = %q / %q", m.Plain, m.HTML)
	}
	if !strings.Contains(strings.Join(strings.Fields(m.Text), " "), "Hello there") || strings.Contains(m.Text, "x{}") || strings.Contains(m.Text, "attachment text") {
		t.Fatalf("text = %q", m.Text)
	}
	if m.Snippet != "Café interview on Monday Hello there" {
		t.Fatalf("snippet = %q", m.Snippet)
	}
}

// A message cut at the fetch bound keeps the parts that came before the cut.
func TestGmail_Message_ACutMessageKeepsItsFirstParts(t *testing.T) {
	raw := "Subject: s\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nfirst part\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>second part, cut"
	var m Message
	m.parse([]byte(raw))
	if m.Subject != "s" || m.Plain != "first part" {
		t.Fatalf("message = %+v", m)
	}
	var deep strings.Builder
	deep.WriteString("Content-Type: multipart/mixed; boundary=d0\r\n\r\n")
	for i := range maxPartDepth + 5 {
		fmt.Fprintf(&deep, "--d%d\r\nContent-Type: multipart/mixed; boundary=d%d\r\n\r\n", i, i+1)
	}
	deep.WriteString("--d25\r\nContent-Type: text/plain\r\n\r\ntoo deep\r\n")
	var d Message
	d.parse([]byte(deep.String()))
	if d.Text != "" {
		t.Fatalf("a part below the depth bound was read: %q", d.Text)
	}
}

// The session never changes the mailbox: folders are examined, bodies are
// peeked, and no other command is sent.
func TestGmail_Client_NeverChangesTheMailbox(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1000)
	s.addJob("a2", 1001)
	s.imap.Search("Q1", "a1", "a2")
	s.run(TriggerCLI)
	s.addJob("a3", 1002)
	s.run(TriggerCLI)
	if _, err := s.client.Find(s.ctx, "Q1", 5); err != nil {
		t.Fatal(err)
	}
	log := s.imap.Commands("")
	for _, l := range log {
		name, _, _ := strings.Cut(l, " ")
		if strings.HasPrefix(l, "UID ") {
			name = l[:strings.Index(l[4:], " ")+4]
		}
		if !slices.Contains(readCommands, name) {
			t.Errorf("command %q is not a read", l)
		}
		if strings.HasPrefix(l, "UID FETCH") && strings.Contains(l, "BODY") && !strings.Contains(l, "BODY.PEEK[]<0.") {
			t.Errorf("a body fetch without PEEK and the size bound: %q", l)
		}
	}
	if len(log) == 0 {
		t.Fatal("no command was logged")
	}
}

// A command outside the read list is refused before it is written, so the
// server never sees it.
func TestGmail_Client_AWriteCommandIsRefusedLocally(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.imap.Add(imaptest.AllMail, msgSpec{ID: "a1", From: "a", Subject: "s", Text: "t", MS: 1})
	sess, err := e.client.open(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.close()
	one := new(imap.SeqSet)
	one.AddNum(1)
	seen := []any{imap.SeenFlag}
	for _, cmd := range []imap.Commander{
		&commands.Select{Mailbox: "INBOX"},
		&commands.Store{SeqSet: one, Item: imap.FormatFlagsOp(imap.AddFlags, true), Value: seen},
		&commands.Uid{Cmd: &commands.Store{SeqSet: one, Item: imap.FormatFlagsOp(imap.AddFlags, true), Value: seen}},
		&commands.Uid{Cmd: &commands.Move{SeqSet: one, Mailbox: "[Gmail]/Trash"}},
		&commands.Copy{SeqSet: one, Mailbox: "Archive"},
		&commands.Expunge{},
	} {
		if err := sess.exec(cmd, nil); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want refused", cmd.Command().Name, err)
		}
	}
	for _, l := range e.imap.Commands("") {
		if !strings.HasPrefix(l, "LOGIN") && !strings.HasPrefix(l, "LIST") && !strings.HasPrefix(l, "EXAMINE") {
			t.Errorf("command %q reached the server", l)
		}
	}
}

// A refused sign-in is recorded once on the account and cleared by the next
// accepted one; its text is Gmail's and never the password.
func TestGmail_Client_LoginRefusedIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.imap.Refuse("Invalid credentials (Failure)")
	_, err := e.client.Find(e.ctx, "x", 1)
	var le *LoginError
	if !errors.As(err, &le) || !IsAuthFailure(err) || !AuthFailureText(err.Error()) {
		t.Fatalf("err = %v, want a credential refusal", err)
	}
	if strings.Contains(err.Error(), testPassword) || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Fatalf("err text = %q", err)
	}
	if got := e.repo.stateOr(e.ctx, keyLoginStatus); got != loginRefused {
		t.Fatalf("login status = %q", got)
	}
	e.imap.Refuse("")
	if _, err := e.client.Find(e.ctx, "x", 1); err != nil {
		t.Fatal(err)
	}
	if got := e.repo.stateOr(e.ctx, keyLoginStatus); got != "" {
		t.Fatalf("login status after an accepted sign-in = %q", got)
	}
}

// Gmail's "too many connections" is not a refusal of the password, and a
// connection that cannot be made is not one either.
func TestGmail_Client_TransientFailuresAreNotRefusals(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.imap.Refuse("Too many simultaneous connections. (Failure)")
	if _, err := e.client.Find(e.ctx, "x", 1); err == nil || IsAuthFailure(err) {
		t.Fatalf("err = %v", err)
	}
	e.client.Dial = func(ctx context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }
	_, err := e.client.Find(e.ctx, "x", 1)
	var ue *UnreachableError
	if !errors.As(err, &ue) || IsAuthFailure(err) || AuthFailureText(err.Error()) {
		t.Fatalf("err = %v, want unreachable", err)
	}
	if got := e.repo.stateOr(e.ctx, keyLoginStatus); got != "" {
		t.Fatalf("login status = %q", got)
	}
}

func TestGmail_Client_NotConnectedMakesNoConnection(t *testing.T) {
	e := newEnv(t)
	if _, err := e.client.Find(e.ctx, "x", 1); !errors.Is(err, ErrNotConnected) || !IsAuthFailure(err) {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.imap.Commands("")); n != 0 {
		t.Fatalf("%d commands reached the server", n)
	}
}

// One FETCH asks for up to fifty bodies, so its deadline runs per message: a
// slow link that keeps delivering is not cut off after one command's time.
func TestGmail_Client_ABodyFetchHasItsDeadlinePerMessage(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.client.timeout = time.Second
	for _, id := range []string{"a1", "a2", "a3"} {
		e.imap.Add(imaptest.AllMail, msgSpec{ID: id, From: "a", Subject: "s", Text: "t", MS: 1})
	}
	e.imap.Search("q", "a1", "a2", "a3")
	e.imap.PaceBodies(400 * time.Millisecond)
	msgs, err := e.client.Find(e.ctx, "q", 3)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("a FETCH slower than one command's time, message by message in time: %d messages, %v", len(msgs), err)
	}
	// A message that does not arrive in time still ends the read.
	e.imap.PaceBodies(3 * time.Second)
	start := time.Now()
	if _, err := e.client.Find(e.ctx, "q", 3); err == nil || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("a stalled FETCH: err = %v after %s", err, time.Since(start))
	}
}

func TestGmail_Client_ACancelledContextEndsALogoutNobodyAnswers(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.client.timeout = 5 * time.Second
	e.imap.IgnoreLogout()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	s, err := e.client.open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.close(); close(done) }()
	eventually(t, "the session logs out", func() bool { return len(e.imap.Commands("LOGOUT")) > 0 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled context did not end the close")
	}
}

func TestGmail_AppPassword_NeverWrittenToFiles(t *testing.T) {
	e := newEnv(t)
	e.connect()
	e.imap.Add(imaptest.AllMail, msgSpec{ID: "a1", From: "a", Subject: "s", Text: "t", MS: 1})
	if _, err := e.client.Find(e.ctx, "x", 1); err != nil {
		t.Fatal(err)
	}
	assertNoSecretOnDisk(t, e, testPassword)
}

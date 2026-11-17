//go:build browser

package apply

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// tb is the part of testing.TB the rig needs, so a child process can build the
// same rig without a test binary's *testing.T.
type tb interface {
	Helper()
	Fatalf(string, ...any)
	Cleanup(func())
	TempDir() string
}

const alexProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  phone: "+1 555 0100"
  address:
    line1: 1 Example Street
    city: Durham
    county: Durham County
    region: NC
    postal_code: "27701"
    country: US
work_authorization:
  authorized_countries: [US]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
  visa_status: Citizen
`

const applicantJSON = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 555 0100",
"address":{"line1":"1 Example Street","city":"Durham","state":"NC","postalCode":"27701","country":"United States"}},
"links":{"linkedin":"https://www.linkedin.com/in/alex-example"},"history":{"employment":[],"education":[]}}`

const resumePath = "../engine/testdata/resume-alex-example.pdf"

// e2eEnv is the machine of an end-to-end run: ready, with the browser the
// shared Chrome provides.
type e2eEnv struct{ win Window }

func (e e2eEnv) Health(context.Context) (int, error)             { return 200, nil }
func (e e2eEnv) AnswerBank(context.Context) (string, int, error) { return "answer-bank.yml", 1, nil }
func (e e2eEnv) Profile(context.Context) (ProfileCheck, error)   { return ProfileCheck{}, nil }
func (e e2eEnv) Attach(context.Context) (Window, error)          { return e.win, nil }

// e2e is one end-to-end rig: a workspace database, the real matcher over the
// fictional profile, the real engine in a throwaway Chrome, and a runner.
type e2e struct {
	st      *store.Store
	ledger  *ledger.Ledger
	tracker *tracker.Tracker
	runner  *Runner
	matches *MatchLog
	db      string
	creds   *credentials.Service
	vault   *vault.Vault
	// applicant and facts, when set, stand in for the fictional Alex's snapshot
	// and profile facts, so a board can be walked as another persona.
	applicant string
	facts     *match.Facts
}

func fastWalk() WalkTiming {
	return WalkTiming{AfterForm: 3 * time.Second, First: 3 * time.Second, Poll: 150 * time.Millisecond, PressWait: 4 * time.Second,
		OpenWait: 5 * time.Second, TerminalWait: 3 * time.Second, ConfirmWait: 3 * time.Second}
}

func fastEngine() engine.Timing {
	return engine.Timing{Poll: 150 * time.Millisecond, PollTimeout: 20 * time.Second, PageDeadline: 60 * time.Second, HungGrace: 5 * time.Second, DocumentWait: 2 * time.Second, ApplicantRetry: 100 * time.Millisecond}
}

// newE2E builds a rig on the lane's window. dir holds the workspace.
func newE2E(t tb, lane *browser.LaneHandle, dir string, hk Hooks) *e2e {
	t.Helper()
	ctx := context.Background()
	ws := workspace.Open(dir)
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatalf("%v", err)
	}
	st, err := store.Open(ctx, filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { st.Close() })
	bank := answerbank.New(ws, st)
	if _, err := bank.Seed(); err != nil {
		t.Fatalf("%v", err)
	}
	if err := os.WriteFile(ws.Path(workspace.FileProfile), []byte(alexProfile), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	matches := &MatchLog{}
	binding := bind.New(bind.Deps{Bank: bank, Workspace: ws, Now: func() time.Time { return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC) }, Observe: matches.Observe})
	tr, err := tracker.New(st, tracker.Options{})
	if err != nil {
		t.Fatalf("%v", err)
	}
	pdf, err := os.ReadFile(resumePath)
	if err != nil {
		t.Fatalf("%v", err)
	}
	payload := engine.ResumePayload{FileName: "resume-alex-example.pdf", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(pdf)}
	v, err := vault.New("e2e-ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatalf("%v", err)
	}
	cs := credentials.New(st, v, nil)
	g := &e2e{creds: cs, vault: v, st: st, ledger: ledger.New(st, nil), tracker: tr, matches: matches, db: filepath.Join(ws.StateDir(), store.DBFile)}
	salt := "e2e-salt-0123456789abcdef0123456789ab"
	g.runner = New(Deps{
		Store: st, Ledger: g.ledger, Audit: NewAudit(st, salt, nil), Reports: SQLReports{DB: st.Read()},
		Env: e2eEnv{win: LaneWindow{Lane: lane}}, Build: "e2e", Salt: salt, Hooks: hk,
		Board: func(BoardUpdate) {},
		Page: PageDeps{
			Applicant: func(context.Context) (json.RawMessage, error) {
				if g.applicant != "" {
					return json.RawMessage(g.applicant), nil
				}
				return json.RawMessage(applicantJSON), nil
			},
			Matcher: binding, Matches: matches,
			Facts: func(context.Context) (match.Facts, error) {
				if g.facts != nil {
					return *g.facts, nil
				}
				return match.Facts{RequiresSponsorshipNow: ptr(false), RequiresSponsorshipFuture: ptr(false)}, nil
			},
			ProfileResume: func(context.Context, *ReportBinding) (*engine.ResumePayload, string, error) {
				p := payload
				return &p, "the default", nil
			},
			Tracker: tr, Timing: fastWalk(), Check: CheckTiming{Reread: 100 * time.Millisecond, Settle: 200 * time.Millisecond}, Engine: fastEngine(),
			Credentials: cs, Account: AccountTiming{Stable: 600 * time.Millisecond, Answer: 15 * time.Second, Poll: 150 * time.Millisecond, Dwell: time.Millisecond},
			StageDir: filepath.Join(dir, "uploads"), SnapshotDir: filepath.Join(dir, "snapshots"), Keep: &Keeper{},
		},
	})
	return g
}

// seedPosting records the evaluated report and tracker row of a posting, so
// the run binds to company and role and the tracker effect is observable.
func (g *e2e) seedPosting(t tb, postingURL string) {
	t.Helper()
	if _, err := g.tracker.Add(context.Background(), tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: postingURL, Status: tracker.Evaluated}); err != nil {
		t.Fatalf("add tracker row: %v", err)
	}
}

func (g *e2e) count(t tb, q string, args ...any) int {
	t.Helper()
	var n int
	if err := g.st.Read().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%v", err)
	}
	return n
}

func (g *e2e) ledgerState(t tb) (state string, confirmed bool, source string) {
	t.Helper()
	var c sql.NullBool
	if err := g.st.Read().QueryRow(`SELECT state, confirmed, COALESCE(confirm_source, '') FROM submissions ORDER BY id DESC LIMIT 1`).Scan(&state, &c, &source); err != nil {
		return "none", false, ""
	}
	return state, c.Bool, source
}

func submitInput(url string) Input {
	return Input{URL: url, Submit: true, ExplicitRequest: true, Caller: gates.CallerCLI}
}

// seedPassword stores a password and its index row, as Settings would.
func (g *e2e) seedPassword(t tb, name, kind, host, pw string) {
	t.Helper()
	if _, err := g.vault.Set(name, pw); err != nil {
		t.Fatalf("%v", err)
	}
	err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), 'alex@example.com', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, name, kind, host)
		return err
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
}

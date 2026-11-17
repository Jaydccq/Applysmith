package apply

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
)

const windowJob = "https://acme.wd5.myworkdayjobs.com/en-US/careers/job/platform-engineer"

const welcomeText = "We have received your application for the Backend Engineer position."

func windowMail(subject string, at time.Time) *gmail.Message {
	return &gmail.Message{
		ID: "m1", ThreadID: "t1", InternalMS: at.UnixMilli(),
		From: "Example Corp <" + mailFrom("acme", "otp.workday.com") + ">", To: "alex@example.com", Subject: subject,
		Snippet: welcomeText, Text: welcomeText, Plain: welcomeText,
	}
}

func saveWindowRun(t *testing.T, g *rig, id string, started time.Time, signins []SigninRecord, attemptAt *time.Time) {
	t.Helper()
	run := &Run{ID: id, StartedAt: started, FinishedAt: started.Add(10 * time.Minute), Input: Input{URL: windowJob}, Signins: signins,
		Outcome: Outcome{Kind: OutcomeStopped}}
	if err := saveRun(context.Background(), g.st, run); err != nil {
		t.Fatal(err)
	}
	if attemptAt == nil {
		return
	}
	err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO signup_attempts (system, host, run_id, at) VALUES ('workday', 'acme.wd5.myworkdayjobs.com', ?, ?)`, id, attemptAt.UTC().Format(stampLayout))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApply_SignupWindows_RunThatCreatedAnAccountDemotesItsWelcomeMail(t *testing.T) {
	g := newRig(t, nil)
	created := t0.Add(2 * time.Minute)
	saveWindowRun(t, g, "run_a", t0, []SigninRecord{{Host: "acme.wd5.myworkdayjobs.com", Succeeded: "created-account"}}, &created)
	// A run that signed in with a stored password created nothing.
	saveWindowRun(t, g, "run_b", t0.Add(24*time.Hour), []SigninRecord{{Host: "other.wd1.myworkdayjobs.com", Succeeded: "stored"}}, nil)

	src := NewRunWindows(g.st, func() time.Time { return g.now })
	ws := src.SignupWindows()
	if len(ws) != 1 || !slices.Contains(ws[0].Hosts, "acme.wd5.myworkdayjobs.com") || !ws[0].From.Equal(t0) {
		t.Fatalf("windows %+v: one window, for the run that created the account", ws)
	}
	if want := t0.Add(10*time.Minute + classify.SignupGrace); !ws[0].Until.Equal(want) {
		t.Fatalf("window ends %s, want the run's end plus the grace %s", ws[0].Until, want)
	}

	tax := classify.Defaults()
	subject := "Verify your email address to confirm your application to Example Corp"
	with := gmail.NewClassifyFunc(tax, src)(windowMail(subject, t0.Add(5*time.Minute)), "alex@example.com")
	if with.Kind != gmail.VerdictSignal || with.RuleID != classify.SelfTriggeredRule || with.Tier != classify.TierLow {
		t.Fatalf("welcome mail inside the window: %+v", with)
	}
	without := gmail.NewClassifyFunc(tax, nil)(windowMail(subject, t0.Add(5*time.Minute)), "alex@example.com")
	if !slices.Contains(without.ReviewReasons, classify.ReviewSignupWindowUnknown) {
		t.Fatalf("with no source the mail reads signup_window_unknown: %+v", without)
	}
	late := gmail.NewClassifyFunc(tax, src)(windowMail(subject, t0.Add(30*time.Hour)), "alex@example.com")
	if late.RuleID == classify.SelfTriggeredRule || slices.Contains(late.ReviewReasons, classify.ReviewSignupWindowUnknown) {
		t.Fatalf("mail long after the window is an employer's: %+v", late)
	}
}

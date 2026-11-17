package learn

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A radio group sent as a text control stores neither the chosen label nor the
// options: the kind is the client's word.
func TestAnswerBank_Withheld_OptionSetSentAsTextStoresNothing(t *testing.T) {
	e := newEnv(t)
	u := withheldFill("kind-text", "Pick one")
	u.ControlKind, u.Options = "text", []string{"Male", "Female", "Prefer not to say"}
	u.OptionLabel, u.After = "Female", "Female"
	if res := e.ingest(u); res.Accepted != 1 {
		t.Fatalf("%+v", res)
	}
	f := e.fillsList()[0]
	if f.Withheld != WithheldSelfID || f.Value != nil || f.OptionLabel != nil || e.dbHolds("Female") || e.dbHolds("Prefer not to say") {
		t.Fatalf("%+v", f)
	}
}

// A value that is itself a self-identification answer is withheld when the
// control has options, even when the option list was cut down to the chosen one.
func TestAnswerBank_Withheld_ValueThatIsASelfIDOption(t *testing.T) {
	for _, c := range []struct {
		name    string
		options []string
		after   string
		label   string
		want    Withheld
	}{
		{"single option as value", []string{"Female"}, "Female", "", WithheldSelfID},
		{"single option as label", []string{"Male"}, "", "Male", WithheldSelfID},
		{"decline label", []string{"Prefer not to say"}, "", "Prefer not to say", WithheldSelfID},
		{"polish option", []string{"Kobieta"}, "Kobieta", "", WithheldSelfID},
		{"ordinary option", []string{"Remote", "Hybrid"}, "Remote", "", WithheldNone},
		{"yes no", []string{"Yes", "No"}, "Yes", "", WithheldNone},
		{"no options keeps the value rule off", nil, "Female", "", WithheldNone},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			u := withheldFill(c.name, "Pick one for "+c.name)
			u.Options, u.After, u.OptionLabel = c.options, c.after, c.label
			if res := e.ingest(u); res.Accepted != 1 {
				t.Fatalf("%+v", res)
			}
			if f := e.fillsList()[0]; f.Withheld != c.want || (c.want != WithheldNone && (f.Value != nil || f.OptionLabel != nil)) {
				t.Fatalf("%+v", f)
			}
			// Nothing of a withheld control is anywhere: not its value, options or question.
			if c.want != WithheldNone && (e.dbHolds(c.after+c.label) || e.dbHolds("Pick one for")) {
				t.Fatal("a withheld control's text reached the database")
			}
		})
	}
}

func TestAnswerBank_Withheld_TelemetryValueThatIsASelfIDOption(t *testing.T) {
	e := newEnv(t)
	row, ok := e.s.rowFor(Event{Kind: KindUserCorrected, Host: "jobs.example.test", Question: "Pick one", ControlKind: "text", Options: []string{"Female"}, Value: "Female", At: baseTime})
	if !ok || row.Withheld != WithheldSelfID || row.Value != nil || len(row.Options) != 0 {
		t.Fatalf("%v %+v", ok, row)
	}
}

func TestWithheld_Terms_SpelledOutAndAbbreviatedSecrets(t *testing.T) {
	for _, q := range []string{"Pass word", "passwd", "pwd", "P a s s w o r d", "Enter your pass word", "Your pwd", "P.a.s.s.w.o.r.d", "p-w-d", "Please enter your P A S S W O R D"} {
		if w, label := classify(q, "text", "", "", nil); w != WithheldPassword || label != "secret:password" {
			t.Errorf("%q: got %q (%s)", q, label, w)
		}
	}
}

// Short terms are whole words: a word that merely contains pin or pwd stays
// learnable. A hyphenated "password-free" is withheld on purpose: the folder
// treats the hyphen as a space, so the word password stands alone, and a label
// that names a password is not worth a learnable question.
func TestWithheld_Terms_ShortTermsAreWordBounded(t *testing.T) {
	for _, q := range []string{"pinned", "Which city do you want pinned", "spin class", "Pinterest profile", "Do you like to spin", "Shipping preference", "Upwind"} {
		if w, label := classify(q, "text", "", "", nil); w != WithheldNone {
			t.Errorf("%q is withheld as %q", q, label)
		}
	}
	for _, q := range []string{"Your PIN", "pin", "Password-free login?"} {
		if w, _ := classify(q, "text", "", "", nil); w != WithheldPassword {
			t.Errorf("%q: %q", q, w)
		}
	}
}

func TestWithheld_Terms_JoinLetterRuns(t *testing.T) {
	for in, want := range map[string]string{
		"p a s s w o r d":   "password",
		"enter p w d here":  "enter pwd here",
		"a b":               "a b",
		"i am a developer":  "i am a developer",
		"u s a b c d":       "usabcd",
		"x pass word y z q": "x pass word yzq",
	} {
		if got := fold(in, false); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// Characters a reader cannot see do not make a user's fill differ from the
// system's write, and do not make an edit out of a re-typed value.
func TestAnswerBank_ManualFills_InvisibleCharactersDoNotDefeatTheSystemWriteCheck(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run := e.issueRun()
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#city", "Springfield"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, value string }{
		{"zero-width space", "Springfield\u200b"},
		{"zero-width joiner", "Spring\u200dfield"},
		{"byte-order mark", "\ufeffSpringfield"},
		{"soft hyphen", "Spring\u00adfield"},
		{"no-break space", "Springfield\u00a0"},
		{"inner no-break space", "\u00a0 SPRINGFIELD \u00a0"},
		{"full-width letters", "\uff33pringfield"},
	} {
		if f := e.fillWith(run, "#c", "Which city "+c.name, c.value, true); f.GroundTruth {
			t.Errorf("%s: the system's own write is not the user's", c.name)
		}
	}
	if f := e.fillWith(run, "#c", "Which city is it", "Springfeld\u200b", true); !f.GroundTruth {
		t.Fatal("a different value is the user's even with an invisible character")
	}
}

func TestSameAnswer_IgnoresInvisibleCharactersCaseAndWhitespace(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Springfield", "Springfield\u200b", true},
		{"Springfield", "Spring\u200dfield", true},
		{"Springfield", "\ufeffspringfield", true},
		{"Springfield", "Spring\u00adfield", true},
		{"Springfield", "  SPRINGFIELD\u00a0", true},
		{"Spring field", "Spring\u00a0field", true},
		{"Springfield", "Springfeld", false},
		{"Sao Paulo", "São Paulo", false},
	} {
		if got := sameAnswer(c.a, c.b); got != c.want {
			t.Errorf("%q vs %q: got %v", c.a, c.b, got)
		}
	}
}

func (e *env) runWith(status string, finishedAgo time.Duration, withFinishedAt bool) string {
	e.t.Helper()
	id := e.issueRun()
	at := stamp(baseTime.Add(-finishedAgo))
	if withFinishedAt {
		e.exec(`UPDATE runs SET status = ?, updated_at = ?, finished_at = ? WHERE id = ?`, status, at, at, id)
	} else {
		e.exec(`UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, status, at, id)
	}
	return id
}

// A run that finished long ago takes no more system writes; a manual fill in
// it is still accepted, because the user finishes a form after autofill ends.
func TestAnswerBank_SystemWrites_RefusedForRunsFinishedLongAgo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, c := range []struct {
		name        string
		status      string
		ago         time.Duration
		finishedAt  bool
		wantRefused bool
	}{
		{"succeeded long ago", "succeeded", 11 * time.Minute, true, true},
		{"failed long ago", "failed", time.Hour, true, true},
		{"cancelled long ago", "cancelled", 25 * time.Hour, true, true},
		{"no finish time, stale update", "succeeded", time.Hour, false, true},
		{"succeeded just now", "succeeded", 5 * time.Minute, true, false},
		{"running", "running", 3 * time.Hour, false, false},
		{"queued", "queued", 3 * time.Hour, false, false},
		{"review required long ago", "review_required", 3 * time.Hour, true, true},
		{"review required just now", "review_required", 5 * time.Minute, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := e.runWith(c.status, c.ago, c.finishedAt)
			err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#city", "Springfield"})
			if c.wantRefused != errors.Is(err, ErrSystemWrite) || (!c.wantRefused && err != nil) {
				t.Fatalf("refused=%v err=%v", c.wantRefused, err)
			}
		})
	}
	run := e.runWith("succeeded", time.Hour, true)
	if f := e.fillWith(run, "#city", "Which city after the run", "Shelbyville", true); !f.GroundTruth {
		t.Fatalf("a manual fill after the run ended is still accepted: %+v", f)
	}
}

// Runs stamped by other packages carry no milliseconds; the grace still holds.
func TestAnswerBank_SystemWrites_RunStampedWithoutMillisecondsIsRead(t *testing.T) {
	e := newEnv(t)
	run := e.issueRun()
	e.exec(`UPDATE runs SET status = 'succeeded', updated_at = ?, finished_at = ? WHERE id = ?`,
		baseTime.Add(-time.Hour).UTC().Format(time.RFC3339), baseTime.Add(-time.Hour).UTC().Format(time.RFC3339), run)
	if err := e.s.RecordSystemWrites(context.Background(), run, SystemWrite{"#c", "Springfield"}); !errors.Is(err, ErrSystemWrite) {
		t.Fatalf("%v", err)
	}
	e.exec(`UPDATE runs SET finished_at = ? WHERE id = ?`, baseTime.Add(-time.Minute).UTC().Format(time.RFC3339), run)
	if err := e.s.RecordSystemWrites(context.Background(), run, SystemWrite{"#c", "Springfield"}); err != nil {
		t.Fatalf("%v", err)
	}
}

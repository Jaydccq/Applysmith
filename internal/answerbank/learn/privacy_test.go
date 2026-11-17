package learn

import (
	"strings"
	"testing"
	"time"
)

// A sentinel is the lowercase and uppercase spelling of one value a test tries
// to smuggle into a stored field.
const sentinel = "ZEBRA-9041"

func holdsSentinel(e *env) bool {
	return e.dbHolds(sentinel) || e.dbHolds(strings.ToLower(sentinel))
}

func sideFieldUpload(question, kind string) Upload {
	uploadSeq++
	return Upload{EventID: eid(question + kind + string(rune('a'+uploadSeq%26))), Kind: kind, Host: "jobs.example.test", Question: question, ControlKind: "text",
		At: baseTime.Add(-time.Hour)}
}

// A value-withheld control stores nothing but its category, whatever field a
// sender puts a value in: an identifier that has no grammar shape or names no
// entry is rejected, and the question, the options and the reason keep no
// value.
func TestAnswerBank_Events_SideFieldsNeverCarryAValue(t *testing.T) {
	run := "run_" + strings.Repeat("a", 24)
	for _, c := range []struct {
		name   string
		mut    func(*Upload)
		reject string
	}{
		{"event id", func(u *Upload) { u.EventID = sentinel }, "invalid-event-id"},
		{"event id lowercase", func(u *Upload) { u.EventID = strings.ToLower(sentinel) }, "invalid-event-id"},
		{"run id", func(u *Upload) { u.RunID = sentinel }, "invalid-run-id"},
		{"run id url", func(u *Upload) { u.RunID = "https://x.example.test/a?token=" + sentinel }, "invalid-run-id"},
		{"run id of a run nobody issued", func(u *Upload) { u.RunID = run }, "unknown-run"},
		{"entry id", func(u *Upload) { u.EntryID = sentinel }, "invalid-entry-id"},
		{"entry id url", func(u *Upload) { u.EntryID = "https://x.example.test/a?token=" + sentinel }, "invalid-entry-id"},
		{"entry id spaces", func(u *Upload) { u.EntryID = "my " + strings.ToLower(sentinel) }, "invalid-entry-id"},
		{"entry id that names no entry", func(u *Upload) { u.EntryID = strings.ToLower(sentinel) + "-secret" }, "unknown-entry-id"},
		{"reason", func(u *Upload) { u.Reason = strings.ToLower(sentinel) }, "invalid-reason"},
		{"options", func(u *Upload) { u.Options = []string{sentinel} }, ""},
		{"question after colon", func(u *Upload) { u.Question = "Password: " + sentinel }, ""},
		{"question after line break", func(u *Upload) { u.Question = "Password\n" + sentinel }, ""},
		{"question past 120 characters", func(u *Upload) { u.Question = "Password " + strings.Repeat("x", 130) + " " + sentinel }, ""},
		{"question past 1000 characters", func(u *Upload) { u.Question = "Password " + strings.Repeat("x", 1100) + " " + sentinel }, "question-too-long"},
		{"gender question", func(u *Upload) { u.Question = "Gender: " + sentinel }, ""},
		{"value before the word", func(u *Upload) { u.Question = sentinel + " password" }, ""},
		{"value after the word", func(u *Upload) { u.Question = "Password " + sentinel }, ""},
		{"value inside a sentence", func(u *Upload) { u.Question = "Enter your password " + sentinel + " here" }, ""},
		{"one-time code sentence", func(u *Upload) { u.Question = "Enter the code we emailed you " + sentinel }, ""},
		{"zero-width space before the colon", func(u *Upload) { u.Question = "Password\u200b: " + sentinel }, ""},
		{"zero-width space instead of the colon", func(u *Upload) { u.Question = "Password\u200b" + sentinel }, ""},
		{"fullwidth colon", func(u *Upload) { u.Question = "Password\uff1a" + sentinel }, ""},
		{"line separator", func(u *Upload) { u.Question = "Password\u2028" + sentinel }, ""},
		{"no-break space", func(u *Upload) { u.Question = "Password\u00a0" + sentinel }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			u := sideFieldUpload("Password", UploadManualFill)
			u.After, u.TrustedInput, u.InputType = "z", true, "password"
			c.mut(&u)
			res := e.ingest(u)
			if c.reject != "" && (len(res.Rejected) != 1 || res.Rejected[0].Reason != c.reject) {
				t.Fatalf("want rejection %q: %+v", c.reject, res)
			}
			if c.reject == "" && res.Accepted != 1 {
				t.Fatalf("%+v", res)
			}
			if holdsSentinel(e) {
				t.Fatal("a value reached the database")
			}
			if c.reject == "" {
				f := e.fillsList()[0]
				if f.Question != "secret:password" || e.count("manual_fills WHERE options_json <> '[]'") != 0 {
					t.Fatalf("only the category is kept, and no options: %+v", f)
				}
				if got := e.count("learning_uploads WHERE question = 'secret:password'"); got != 1 {
					t.Fatalf("the upload record keeps the category too: %d", got)
				}
			}
		})
	}
}

func TestAnswerBank_Events_UncertaintyReasonIsClosed(t *testing.T) {
	e := newEnv(t)
	u := uncertainty("jobs.example.test", "Password", strings.ToLower(sentinel))
	if res := e.ingest(u); len(res.Rejected) != 1 || res.Rejected[0].Reason != "invalid-reason" || holdsSentinel(e) {
		t.Fatalf("%+v", res)
	}
	for _, r := range uploadReasons {
		if res := e.ingest(uncertainty("jobs.example.test", "Question "+r, r)); res.Accepted != 1 {
			t.Errorf("%q: %+v", r, res)
		}
	}
}

// Telemetry written by the server's own callers follows the same rules: the
// category replaces the question, and the withheld rows feed no candidate.
func TestAnswerBank_Telemetry_WithheldControlKeepsOnlyTheCategory(t *testing.T) {
	e := newEnv(t)
	run := e.issueRun()
	e.record(
		Event{Kind: KindUserCorrected, Host: "jobs.example.test", Question: "Password: " + sentinel, ControlKind: "text", InputType: "password", Options: []string{sentinel},
			EntryID: "pw", Lane: LaneManual, RunID: run},
		Event{Kind: KindUserCorrected, Host: "jobs.example.test", Question: "Anything", ControlKind: "text", EntryID: sentinel, RunID: "https://x.example.test/?t=" + sentinel},
	)
	if holdsSentinel(e) {
		t.Fatal("a value reached the database")
	}
	if e.count("answer_events WHERE entry_id = 'pw' AND run_id = ? AND question = 'secret:password' AND options_json = '[]'", run) != 1 {
		t.Fatal("the category, the entry and the run are kept")
	}
}

func TestAnswerBank_Withheld_DisguisedSecretsAndCodes(t *testing.T) {
	for _, c := range []struct {
		name, q, kind, inputType, autocomplete string
		want                                   Withheld
	}{
		{"PIN", "Enter your PIN", "text", "", "", WithheldPassword},
		{"bare PIN", "PIN", "text", "", "", WithheldPassword},
		{"passcode", "Your passcode", "text", "", "", WithheldPassword},
		{"secret phrase", "Enter your secret phrase", "text", "", "", WithheldPassword},
		{"security question answer", "Answer to your security question", "text", "", "", WithheldPassword},
		{"code we sent", "Enter the code we sent you", "text", "", "", WithheldOneTimeCode},
		{"code we emailed", "Enter the code we emailed you", "text", "", "", WithheldOneTimeCode},
		{"code we texted", "The code we texted you", "text", "", "", WithheldOneTimeCode},
		{"code from your phone", "Code from your phone", "text", "", "", WithheldOneTimeCode},
		{"bare code", "Code", "text", "", "", WithheldOneTimeCode},
		{"bare enter the code", "Enter the code:", "text", "", "", WithheldOneTimeCode},
		{"bare token", "Token *", "text", "", "", WithheldOneTimeCode},
		{"current-password on a text input", "Your account", "text", "text", "current-password", WithheldPassword},
		{"new-password", "Choose one", "text", "", "new-password", WithheldPassword},
		{"new-password among tokens", "Choose one", "text", "", "section-login new-password", WithheldPassword},
		{"one-time-code", "Your account", "text", "", "one-time-code", WithheldOneTimeCode},
		{"password input type", "Your account", "text", "password", "", WithheldPassword},
		{"what do you go by", "What do you go by?", "text", "", "", WithheldSelfID},
		{"how do you identify", "How do you identify?", "text", "", "", WithheldSelfID},
		{"postal code is not a secret", "Postal code", "text", "", "", WithheldNone},
		{"promo code is not a secret", "Promo code", "text", "", "", WithheldNone},
		{"captcha is not a secret", "Type the code shown", "text", "", "", WithheldNone},
		{"area code is not a secret", "Area code", "text", "", "", WithheldNone},
		{"opinion about pins", "Which tool do you prefer for pinning dependencies?", "text", "", "", WithheldNone},
	} {
		if got := Classify(c.q, c.kind, c.inputType, c.autocomplete, nil); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

// The classifier reuses the matcher's protected-characteristic predicate: every
// phrase the matcher calls sensitive is withheld here, so the two cannot drift.
func TestAnswerBank_Withheld_AgreesWithTheMatcherPredicate(t *testing.T) {
	for _, q := range []string{"Veteran status", "Marital status", "Sexual orientation", "What is your race?", "Do you have a disability?",
		"Your religion", "Citizenship status", "Are you pregnant?", "Your age", "Gender identity", "Date of birth"} {
		if got := Classify(q, "text", "", "", nil); got == WithheldNone {
			t.Errorf("%q is a protected characteristic and is not withheld", q)
		}
	}
}

func TestAnswerBank_ManualFills_DisguisedLabelsDropValue(t *testing.T) {
	e := newEnv(t)
	var ups []Upload
	for i, c := range []struct {
		q, inputType, autocomplete string
	}{
		{"Enter your secret phrase", "", ""}, {"Enter your PIN", "", ""}, {"Enter the code we emailed you", "", ""}, {"Code", "", ""},
		{"Your account", "text", "current-password"}, {"Your account", "text", "new-password"}, {"Your account", "", "one-time-code"}, {"What do you go by?", "", ""},
	} {
		u := fillUpload("jobs.example.test", c.q, "")
		u.EventID, u.After, u.InputType, u.Autocomplete = eid(c.q+c.autocomplete+string(rune('a'+i))), sentinel+"-"+string(rune('a'+i)), c.inputType, c.autocomplete
		ups = append(ups, u)
	}
	if res := e.ingest(ups...); res.Accepted != len(ups) || len(res.Rejected) != 0 {
		t.Fatalf("%+v", res)
	}
	if holdsSentinel(e) {
		t.Fatal("a value of a disguised secret reached the database")
	}
	for _, f := range e.fillsList() {
		if f.Value != nil || f.Withheld == WithheldNone {
			t.Errorf("%+v", f)
		}
	}
}

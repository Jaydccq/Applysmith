package learn

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func withheldFill(label, question string) Upload {
	u := fillUpload("jobs.example.test", question, sentinel)
	u.EventID = eid("regression-" + label)
	u.At = baseTime.Add(-time.Hour)
	return u
}

// A question in another language, or one with the value tucked beside the
// label, is classified from its words and stored as its category only.
func TestAnswerBank_Withheld_ForeignSelfIDQuestionsStoreOnlyTheCategory(t *testing.T) {
	for _, c := range []struct{ name, q, want string }{
		{"german gender", "Geschlecht", "self-id:gender"},
		{"spanish sex", "Sexo", "self-id:gender"},
		{"spanish gender", "Género", "self-id:gender"},
		{"chinese gender", "\u6027\u522b", "self-id:gender"},
		{"german orientation", "Sexuelle Orientierung", "self-id:orientation"},
		{"german ethnicity", "Ethnie", "self-id:ethnicity"},
		{"german disability", "Behinderung", "self-id:disability"},
		{"german veteran", "Veteranenstatus", "self-id:veteran"},
		{"fullwidth colon", "Gender\uff1a" + sentinel, "self-id:gender"},
		{"value before the word", sentinel + " password", "secret:password"},
		{"emailed code", "Enter the code we emailed you " + sentinel, "secret:one-time-code"},
		{"zero-width space", "Password\u200b" + sentinel, "secret:password"},
		{"zero-width space before a colon", "Password\u200b: " + sentinel, "secret:password"},
		{"fullwidth colon on a password", "Password\uff1a" + sentinel, "secret:password"},
		{"line separator", "Password\u2028" + sentinel, "secret:password"},
		{"no-break space", "Password\u00a0" + sentinel, "secret:password"},
		{"value inside a sentence", "Enter your password " + sentinel + " here", "secret:password"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if res := e.ingest(withheldFill(c.name, c.q)); res.Accepted != 1 {
				t.Fatalf("%+v", res)
			}
			f := e.fillsList()[0]
			if f.Question != c.want || f.Value != nil || f.OptionLabel != nil || holdsSentinel(e) {
				t.Fatalf("%+v", f)
			}
			if e.count("learning_uploads WHERE question = ?", c.want) != 1 {
				t.Fatal("the upload record keeps the category too")
			}
		})
	}
}

// A select whose options are a self-identification set is withheld whatever
// its question says.
func TestAnswerBank_Withheld_ChineseSelectByItsOptions(t *testing.T) {
	e := newEnv(t)
	u := withheldFill("options", "\u4f60\u7684\u8eab\u4efd")
	u.ControlKind, u.Options = "select", []string{"\u7537", "\u5973", "\u4e0d\u613f\u900f\u9732"}
	u.OptionLabel, u.After = sentinel, sentinel
	if res := e.ingest(u); res.Accepted != 1 {
		t.Fatalf("%+v", res)
	}
	if f := e.fillsList()[0]; f.Withheld != WithheldSelfID || f.Value != nil || f.OptionLabel != nil || holdsSentinel(e) {
		t.Fatalf("%+v", f)
	}
}

func reportUpload(label, question string, r Report) Upload {
	return Upload{EventID: eid("regression-report-" + label), Kind: UploadUserReported, Host: "jobs.example.test", Question: question, ControlKind: "text",
		At: baseTime.Add(-time.Hour), Report: &r}
}

func storedReport(t *testing.T, e *env, label string) (Report, string) {
	t.Helper()
	var raw string
	if err := e.st.Read().QueryRow(`SELECT report_json FROM learning_uploads WHERE event_id = ?`, eid("regression-report-"+label)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r, raw
}

// A user report about a withheld control keeps its tag, role, input type and
// category, and the user's own note; every other field of the element is
// dropped, since each can carry what the control holds.
func TestAnswerBank_Withheld_ReportKeepsOnlyTheElementShape(t *testing.T) {
	e := newEnv(t)
	r := Report{Note: "wrong field", Tag: "input", Role: "textbox", AccessibleName: sentinel, Path: "form > " + sentinel, NearbyText: sentinel,
		URL: "https://jobs.example.test/" + sentinel, FrameURL: "https://frames.example.test/" + sentinel, ShadowDepth: 2,
		Snippet: `<input type="password" id="` + sentinel + `" placeholder="` + sentinel + `" data-automation-id="` + sentinel + `" name="pw" aria-label="` + sentinel + `">`}
	if res := e.ingest(reportUpload("pw", "Password "+sentinel, r)); res.Accepted != 1 {
		t.Fatalf("%+v", res)
	}
	if holdsSentinel(e) {
		t.Fatal("an attribute of a password element reached the database")
	}
	got, _ := storedReport(t, e, "pw")
	if got.Note != "wrong field" || got.Tag != "input" || got.Role != "textbox" || got.InputType != "password" || got.Withheld != "secret:password" ||
		got.Snippet != "" || got.AccessibleName != "" || got.Path != "" || got.NearbyText != "" || got.URL != "" || got.FrameURL != "" || got.ShadowDepth != 0 {
		t.Fatalf("%+v", got)
	}
	// The type of the element withholds a report even when the label says nothing.
	if res := e.ingest(reportUpload("type", "Your account", Report{Note: "n", Snippet: `<input type="password" placeholder="` + sentinel + `">`})); res.Accepted != 1 || holdsSentinel(e) {
		t.Fatalf("%+v", res)
	}
	if got, _ := storedReport(t, e, "type"); got.Withheld != "secret:password" || got.Snippet != "" {
		t.Fatalf("%+v", got)
	}
	// A tag or role outside the closed vocabulary is not text the page can use.
	if res := e.ingest(reportUpload("shape", "Password", Report{Note: "n", Tag: sentinel, Role: strings.ToLower(sentinel)})); res.Accepted != 1 || holdsSentinel(e) {
		t.Fatalf("%+v", res)
	}
}

// An entry id must name an entry of the user's bank or of the defaults bank.
func TestAnswerBank_Events_EntryIdMustExist(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("quill-feather", "x", nil))
	mk := func(label, entry string) Upload {
		u := fillUpload("jobs.example.test", "Preferred shift", "Nights")
		u.EventID, u.Before, u.EntryID = eid("regression-entry-"+label), "Days", entry
		return u
	}
	res := e.ingest(mk("user", "quill-feather"), mk("defaults", "address-city"), mk("slug", "zebra-h12-secret"), mk("renamed", "quill-feather-2"))
	if res.Accepted != 2 || len(res.Rejected) != 2 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res.Rejected {
		if r.Reason != "unknown-entry-id" {
			t.Errorf("%+v", r)
		}
	}
	if e.dbHolds("zebra-h12-secret") {
		t.Fatal("an unknown id reached the database")
	}
}

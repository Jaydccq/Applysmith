package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/credentials"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/imaptest"
	"applysmith/internal/workspace"
)

func longText(word string, n int) string { return strings.Repeat(word+" ", n) }

func TestStory_NarrativeNeedsLengthAndNoPlaceholdersAndVoiceIsOptional(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "story"); d.Status != StepTemplate {
		t.Fatalf("seed text: %+v", d)
	}
	mustApply(t, env, "story", `{"narrative":"Too short."}`)
	if d := status(t, env, "story"); d.Status != StepPartial || !hasReason(d, "at least 300") {
		t.Fatalf("short narrative: %+v", d)
	}
	mustApply(t, env, "story", `{"narrative":"`+longText("practical", 60)+`[Your Name]"}`)
	if d := status(t, env, "story"); d.Status != StepTemplate || !hasReason(d, "[Your Name]") {
		t.Fatalf("placeholder: %+v", d)
	}
	mustApply(t, env, "story", `{"narrative":"`+longText("practical", 60)+`"}`)
	d := status(t, env, "story")
	if d.Status != StepComplete || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "voice") {
		t.Fatalf("complete narrative, no voice: %+v", d)
	}
	if err := Skip(bg(), env, voiceSkipID); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "story"); d.Status != StepComplete || !strings.Contains(d.Warnings[0], "skipped") {
		t.Fatalf("voice skipped: %+v", d)
	}
	if err := Skip(bg(), env, "story"); err == nil {
		t.Error("the required story step was skipped")
	}
	// Supplying samples ends the skip.
	mustApply(t, env, "story", `{"voice":"`+longText("direct", 100)+`"}`)
	if d := status(t, env, "story"); len(d.Warnings) != 0 {
		t.Fatalf("voice in place: %+v", d)
	}
}

func TestResumes_RosterNeedsAnExistingPDFAndExactlyOneDefault(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "resumes"); d.Status != StepMissing {
		t.Fatalf("empty roster: %+v", d)
	}
	pdf := filepath.Join(t.TempDir(), "general.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := mustApply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"general","label":"General"}}`)
	if res.Detection.Status != StepComplete {
		t.Fatalf("after add: %+v", res.Detection)
	}
	if err := os.Remove(filepath.Join(env.Workspace.Root, "resumes", "general.pdf")); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "resumes"); d.Status != StepPartial || !hasReason(d, "resumes/general.pdf") {
		t.Fatalf("dangling entry: %+v", d)
	}
	_, err := apply(t, env, "resumes", `{"roster":[{"id":"a","label":"A","file":"resumes/a.pdf","default":true},{"id":"b","label":"B","file":"resumes/b.pdf","default":true}]}`, false)
	if err == nil || !strings.Contains(err.Error(), "exactly one default") {
		t.Fatalf("two defaults: %v", err)
	}
	if _, err := apply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"x"},"roster":[]}`, false); err == nil {
		t.Error("add and roster together were accepted")
	}
	notPDF := filepath.Join(t.TempDir(), "x.pdf")
	_ = os.WriteFile(notPDF, []byte("hello"), 0o600)
	if _, err := apply(t, env, "resumes", `{"add":{"path":"`+notPDF+`","id":"x"}}`, false); err == nil {
		t.Error("a file that is not a PDF was stored")
	}
}

// A dry run of add checks the source PDF and the roster it would make without
// copying anything, so it passes exactly when the real add would.
func TestResumes_DryRunAddChecksTheSourceAndStoresNothing(t *testing.T) {
	env := setupEnv(t)
	pdf := filepath.Join(t.TempDir(), "general.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile))
	res, err := apply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"general","label":"General"}}`, true)
	if err != nil || !res.DryRun || res.Wrote {
		t.Fatalf("dry add: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace.Root, "resumes", "general.pdf")); !os.IsNotExist(err) {
		t.Fatalf("a dry run stored the file: %v", err)
	}
	if after, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile)); string(after) != string(before) {
		t.Fatal("a dry run wrote profile.yml")
	}
	notPDF := filepath.Join(t.TempDir(), "x.pdf")
	_ = os.WriteFile(notPDF, []byte("hello"), 0o600)
	if _, err := apply(t, env, "resumes", `{"add":{"path":"`+notPDF+`","id":"x"}}`, true); err == nil || !strings.Contains(err.Error(), "not a PDF") {
		t.Fatalf("dry add of a non-PDF: %v", err)
	}
	if _, err := apply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"Bad Id"}}`, true); err == nil || !strings.Contains(err.Error(), "add.id") {
		t.Fatalf("dry add with an invalid id: %v", err)
	}
	mustApply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"general"}}`)
	if _, err := apply(t, env, "resumes", `{"add":{"path":"`+pdf+`","id":"general"}}`, true); err == nil {
		t.Fatal("a dry add of an id already in the roster passed")
	}
}

// A file the process is not let read (macOS holds the read while it asks
// whether Applysmith may open ~/Desktop) fails the add with what to do, instead
// of holding the request.
func TestResumes_AReadThatDoesNotFinishFailsWithTheFix(t *testing.T) {
	env := setupEnv(t)
	release := make(chan struct{})
	defer close(release)
	prevRead, prevWait := readResumeFile, resumeReadTimeout
	readResumeFile = func(string) ([]byte, error) { <-release; return nil, errors.New("late") }
	resumeReadTimeout = 50 * time.Millisecond
	defer func() { readResumeFile, resumeReadTimeout = prevRead, prevWait }()
	body, _ := jsonString(map[string]any{"add": map[string]string{"path": filepath.Join(env.HomeDir, "Desktop", "cv.pdf"), "id": "general"}})
	start := time.Now()
	_, err := apply(t, env, "resumes", body, false)
	if err == nil || !strings.Contains(err.Error(), "reading ~/Desktop/cv.pdf did not finish") || !strings.Contains(err.Error(), "Files and Folders") || !strings.Contains(err.Error(), "choose a file elsewhere") {
		t.Fatalf("held read: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the add waited %s", time.Since(start))
	}
	readResumeFile = func(string) ([]byte, error) { return nil, &os.PathError{Op: "open", Path: "x", Err: os.ErrPermission} }
	if _, err := apply(t, env, "resumes", body, false); err == nil || !strings.Contains(err.Error(), "did not let Applysmith read ~/Desktop/cv.pdf") {
		t.Fatalf("refused read: %v", err)
	}
}

// A profile.yml written by a YAML tool that shares a repeated list through an
// anchor stays editable by every step: a step beside the shared list keeps
// the anchor, and a step that writes the list writes the alias out first, so
// the other key keeps its value.
func TestStepWritersEditAProfileWithAnchorsAndAliases(t *testing.T) {
	env := setupEnv(t)
	path := env.Workspace.Path(workspace.FileProfile)
	src, _ := os.ReadFile(path)
	text := strings.Replace(string(src), "  locations: []", "  locations: &id001\n  - Leeds\n  - York", 1)
	text = strings.Replace(text, "    places: []", "    places: *id001", 1)
	if text == string(src) || os.WriteFile(path, []byte(text), 0o600) != nil {
		t.Fatal("could not seed the anchored profile")
	}
	read := func() (workspace.Profile, string) {
		t.Helper()
		got, _ := os.ReadFile(path)
		p, rep := workspace.ParseProfile(got)
		if !rep.OK() {
			t.Fatalf("%s\n%s", rep, got)
		}
		return p, string(got)
	}
	mustApply(t, env, "identity", identityBody("Riley Tester"))
	if p, got := read(); p.Candidate.FullName != "Riley Tester" || !strings.Contains(got, "places: *id001") {
		t.Fatalf("identity beside the anchor:\n%s", got)
	}
	mustApply(t, env, "targets", `{"roles":[{"title":"Backend Engineer","seniority":"senior"}],"locations":["Leeds","York","Hull"]}`)
	p, got := read()
	if !slices.Equal(p.Targets.Locations, []string{"Leeds", "York", "Hull"}) || !slices.Equal(p.Preferences.Relocation.Places, []string{"Leeds", "York"}) {
		t.Fatalf("targets on the shared list: %v %v\n%s", p.Targets.Locations, p.Preferences.Relocation.Places, got)
	}
	if strings.Contains(got, "id001") {
		t.Fatalf("anchor left behind:\n%s", got)
	}
}

// Answering an entry the bank ships as a placeholder makes it a real answer;
// leaving it to the user (the ask-me marker) is not an answer and keeps it one.
func TestAnswerBank_AnsweringAPlaceholderClearsTheFlag(t *testing.T) {
	env := setupEnv(t)
	if _, err := bankService(env).Seed(); err != nil {
		t.Fatal(err)
	}
	loaded, err := bankService(env).File()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hourly-pay-rate", "address-line2", "former-employer"} {
		if e, ok := loaded.Bank.Find(id); !ok || !e.Placeholder {
			t.Fatalf("%s is not a shipped placeholder: %+v", id, e)
		}
	}
	mustApply(t, env, "answer-bank", `{"answers":{"hourly-pay-rate":"45","address-line2":"Flat 2","former-employer":"`+match.AskMe+`"}}`)
	loaded, err = bankService(env).File()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"hourly-pay-rate": false, "address-line2": false, "former-employer": true} {
		if e, _ := loaded.Bank.Find(id); e.Placeholder != want {
			t.Errorf("%s: placeholder = %v, want %v", id, e.Placeholder, want)
		}
	}
}

func TestAnswerBank_BlockingEntriesAreCountedAndTheStepClosesWhenAnsweredAndConfirmed(t *testing.T) {
	env := setupEnv(t)
	if _, err := bankService(env).Seed(); err != nil {
		t.Fatal(err)
	}
	mustApply(t, env, "identity", identityBody("Riley Tester"))
	d := status(t, env, "answer-bank")
	if d.Status != StepPartial || len(d.Reasons) < 2 || !strings.HasSuffix(d.Reasons[0].Text, "blocking entries remain") || d.Reasons[0].Field != "blocking" {
		t.Fatalf("fresh bank: %+v", d)
	}
	answers := map[string]string{}
	for _, r := range d.Reasons[1:] {
		if i := strings.LastIndex(r.Text, "("); i >= 0 && r.Field != "" && !strings.Contains(r.Text, "your confirmation") {
			answers[strings.TrimSuffix(r.Text[i+1:], ")")] = match.AskMe
		}
	}
	if len(answers) == 0 {
		t.Fatalf("no blocking entry was listed: %+v", d)
	}
	// The step counts exactly the blocking entries the Answer Bank page lists
	// (both read the stamped bank): the identity step's values answer their
	// entries there, never as a copy the step would still have to write.
	report, err := bankService(env).Unanswered()
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%d blocking entries remain", len(report.Blocking())); d.Reasons[0].Text != want || len(answers) != len(report.Blocking()) {
		t.Fatalf("step says %q and lists %d, the page counts %d", d.Reasons[0].Text, len(answers), len(report.Blocking()))
	}
	for _, id := range []string{"applicant-full-name", "email-address", "phone-mobile", "address-city"} {
		if _, ok := answers[id]; ok {
			t.Fatalf("%s blocks although the identity step answers it", id)
		}
	}
	before, _ := os.ReadFile(env.Workspace.Path(workspace.FileAnswerBank))
	if res := mustApply(t, env, "answer-bank", `{}`); res.Wrote {
		t.Fatal("an empty apply wrote the bank")
	}
	if after, _ := os.ReadFile(env.Workspace.Path(workspace.FileAnswerBank)); string(after) != string(before) {
		t.Fatal("an empty apply copied profile values into the bank")
	}
	if _, err := apply(t, env, "answer-bank", `{"answers":{"no-such-entry":"x"}}`, false); err == nil || !strings.Contains(err.Error(), "answers.no-such-entry") {
		t.Fatalf("unknown entry: %v", err)
	}
	body, _ := jsonString(map[string]any{"answers": answers, "confirm_review": true})
	res := mustApply(t, env, "answer-bank", body)
	if res.Detection.Status != StepComplete {
		t.Fatalf("after answering: %+v", res.Detection)
	}
}

func TestBrowser_PassesOnlyWhenBothWindowsAreAttachedAndQuiet(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "browser"); d.Status != StepMissing {
		t.Fatalf("unchecked: %+v", d)
	}
	var h *HumanError
	if _, err := apply(t, env, "browser", `{}`, false); !errors.As(err, &h) {
		t.Fatalf("without a browser the step needs a person: %v", err)
	}
	f, tr := false, true
	report := BrowserReport{Windows: []WindowReport{{Lane: "apply", Attached: true, Webdriver: &f, UserAgent: "Chrome/155"}, {Lane: "scan", Attached: true, Webdriver: &tr, UserAgent: "Chrome/155"}}}
	env.BrowserCheck = func(context.Context) (BrowserReport, error) { return report, nil }
	res := mustApply(t, env, "browser", `{}`)
	if res.Detection.Status != StepPartial || !hasReason(res.Detection, "scan window reports navigator.webdriver true") {
		t.Fatalf("webdriver true: %+v", res.Detection)
	}
	report.Windows[1].Webdriver = &f
	report.Windows[0].UserAgent = "HeadlessChrome/155"
	if res = mustApply(t, env, "browser", `{}`); !hasReason(res.Detection, "apply window's user agent contains a headless marker") {
		t.Fatalf("headless: %+v", res.Detection)
	}
	report.Windows[0].UserAgent = "Chrome/155"
	if res = mustApply(t, env, "browser", `{}`); res.Detection.Status != StepComplete {
		t.Fatalf("clean: %+v", res.Detection)
	}
}

func TestConnections_ReportMissingUntilTheirFactsExist(t *testing.T) {
	env := setupEnv(t)
	for _, id := range []string{"gmail", "telegram", "credentials", "captcha", "extension"} {
		if d := status(t, env, id); d.Status != StepMissing && d.Status != StepPartial {
			t.Errorf("%s on a fresh workspace: %+v", id, d)
		}
	}
	var h *HumanError
	for _, id := range []string{"gmail", "telegram", "credentials", "captcha", "extension"} {
		if _, err := apply(t, env, id, `{}`, false); !errors.As(err, &h) {
			t.Errorf("%s: an empty apply must say a person is needed, got %v", id, err)
		}
	}
	if _, err := env.Vault.Set(secretCaptchaKey, "k"+longText("x", 20)); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "captcha"); d.Status != StepComplete {
		t.Fatalf("captcha with a key: %+v", d)
	}
	if err := env.setDecision(bg(), extensionCheckinKey, env.now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "extension"); d.Status != StepComplete {
		t.Fatalf("extension after a check-in: %+v", d)
	}
}

// The Gmail step reads the connection: missing until an app password is
// stored, complete while Gmail accepts it, and a reason on the password once
// Gmail refuses it.
func TestGmail_TheStepReadsTheConnection(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "gmail"); d.Status != StepMissing {
		t.Fatalf("not connected: %+v", d)
	}
	address, password := "alex"+"@example.org", strings.Repeat("wxyz", 4)
	srv, err := imaptest.New(address, password)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	stack := gmail.Open(env.Store, env.Vault, gmail.Options{Dial: srv.Dial})
	if _, err := stack.Service.Connect(bg(), address, password); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "gmail"); d.Status != StepComplete {
		t.Fatalf("connected: %+v", d)
	}
	srv.Refuse("Invalid credentials (Failure)")
	if _, err := stack.Service.Test(bg()); err == nil {
		t.Fatal("a refused sign-in passed the test")
	}
	if d := status(t, env, "gmail"); d.Status != StepPartial || len(d.Reasons) != 1 || d.Reasons[0].Field != "password" {
		t.Fatalf("refused: %+v", d)
	}
}

// The credentials step is complete once a shared default sign-in (a password
// and an email) is stored, or Sign in with Google is on; the reason names the
// Settings section.
func TestCredentials_CompleteWithTheSharedDefault(t *testing.T) {
	for _, tc := range []struct {
		name            string
		password, email bool
		google          bool
		status          StepStatus
	}{
		{name: "nothing done", status: StepMissing},
		{name: "a password with no email", password: true, status: StepMissing},
		{name: "the default", password: true, email: true, status: StepComplete},
		{name: "Sign in with Google", google: true, status: StepComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupEnv(t)
			creds := credentials.New(env.Store, env.Vault, env.now)
			if tc.password {
				if _, err := creds.SetDefault(bg(), "", "Correct-Horse-9-battery"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.email {
				if _, err := creds.SetDefault(bg(), goodEmail(), ""); err != nil {
					t.Fatal(err)
				}
			}
			if tc.google {
				on := true
				if _, err := bankService(env).PatchSettings(bg(), answerbank.SettingsPatch{AllowGoogleSignIn: &on}); err != nil {
					t.Fatal(err)
				}
			}
			d := status(t, env, "credentials")
			if d.Status != tc.status {
				t.Fatalf("status %s, reasons %+v; want %s", d.Status, d.Reasons, tc.status)
			}
			if tc.status != StepComplete && (len(d.Reasons) != 1 || !strings.Contains(d.Reasons[0].Text, "Settings, Sign-in credentials")) {
				t.Fatalf("reasons %+v", d.Reasons)
			}
			r := run(t, env, "credentials.default")
			if (r.Status != Pass) != (tc.status != StepComplete) {
				t.Error("credentials.default disagrees with the step")
			}
			// The command reads a password only with --password-stdin, so the
			// fix and the step's command name it.
			const cmd = "applysmith credentials set-default --email <address> --password-stdin"
			if r.Status != Pass && !strings.Contains(r.Fix, cmd) {
				t.Errorf("fix %q does not name a command that stores the password", r.Fix)
			}
			if s := mustStep("credentials"); !slices.Contains(s.Commands, cmd) || !strings.Contains(s.Human, cmd) {
				t.Errorf("commands %q, human %q", s.Commands, s.Human)
			}
		})
	}
}

func TestOrientation_ClosesOnlyWithTheAcknowledgement(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "orientation"); d.Status != StepPartial {
		t.Fatalf("before: %+v", d)
	}
	if err := Ack(bg(), env, "orientation"); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "orientation"); d.Status != StepComplete {
		t.Fatalf("after: %+v", d)
	}
}

// Every leaf the specification names exists, and no leaf belongs to a group
// that is neither a step nor one of the two groups outside the catalog.
func TestDoctorHasEveryLeafOfTheCatalog(t *testing.T) {
	want := strings.Fields(`prerequisites.platform prerequisites.chrome prerequisites.disk workspace.location workspace.files workspace.migrations
		identity.fields cv.content work-authorization.answers targets.fields story.narrative story.voice resumes.files answer-bank.blocking
		llm.provider llm.test-age browser.apply-window browser.fingerprint browser.signins gmail.login gmail.fetch
		telegram.token telegram.pairing credentials.default credentials.vault captcha.key discovery.sources discovery.registry discovery.schedules discovery.schedule-hygiene discovery.schedule-audit
		extension.pairing orientation.ack runtime.server-lock runtime.port runtime.keychain`)
	have := map[string]Check{}
	for _, c := range Checks() {
		have[c.ID] = c
		if _, ok := StepByID(c.Group()); !ok && c.Group() != "runtime" && c.Group() != "agent" {
			t.Errorf("leaf %s belongs to no step", c.ID)
		}
	}
	for _, id := range want {
		if _, ok := have[id]; !ok {
			t.Errorf("leaf %s is missing", id)
		}
	}
	for _, id := range []string{"story.voice", "browser.signins"} {
		if have[id].Required {
			t.Errorf("%s must not be required", id)
		}
	}
	for _, s := range Catalog() {
		if !hasCheck(s.ID) {
			t.Errorf("step %s has no doctor check", s.ID)
		}
	}
}

func TestDoctor_ASkippedOptionalStepReportsSkipped(t *testing.T) {
	env := setupEnv(t)
	if err := Skip(bg(), env, "gmail"); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(bg(), Checks(), env, Options{Only: "gmail"})
	if err != nil || len(rep.Results) != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, r := range rep.Results {
		if r.Status != Skipped {
			t.Errorf("%s is %s", r.ID, r.Status)
		}
	}
	// Not skipped, never set up: the leaves warn and the exit code stays 0.
	rep, _ = Run(bg(), Checks(), env, Options{Only: "telegram"})
	if rep.ExitCode() != ExitOK || rep.Results[0].Status != Warn {
		t.Errorf("telegram: %+v", rep)
	}
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// A choice question takes one of its choices, written with the wordings a form
// offers it under; an answer outside them is refused and nothing is written.
func TestAnswerBank_AChoiceQuestionTakesOneOfItsChoices(t *testing.T) {
	env := setupEnv(t)
	if _, err := bankService(env).Seed(); err != nil {
		t.Fatal(err)
	}
	mustApply(t, env, "answer-bank", `{"answers":{"pronouns":"she/her"}}`)
	loaded, err := bankService(env).File()
	if err != nil {
		t.Fatal(err)
	}
	e, _ := loaded.Bank.Find("pronouns")
	want, ok := answerbank.ShapeOf(e).ChoiceFor("She/her")
	if !ok || len(e.Preferred) == 0 || !slices.Equal(e.Preferred, want.Matches) {
		t.Fatalf("pronouns preferred = %q, want %q", e.Preferred, want.Matches)
	}
	before, _ := os.ReadFile(env.Workspace.Path(workspace.FileAnswerBank))
	_, err = apply(t, env, "answer-bank", `{"answers":{"salutation":"Sir"}}`, false)
	if fe, ok := err.(FieldErrors); !ok || !strings.Contains(fe["answers.salutation"], "one of") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(env.Workspace.Path(workspace.FileAnswerBank)); string(after) != string(before) {
		t.Fatal("a refused answer was written")
	}
	// A dry run refuses what the real run refuses.
	_, err = apply(t, env, "answer-bank", `{"answers":{"salutation":"Sir"}}`, true)
	if fe, ok := err.(FieldErrors); !ok || !strings.Contains(fe["answers.salutation"], "one of") {
		t.Fatalf("dry run: err = %v", err)
	}
}

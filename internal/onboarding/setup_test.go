package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/browser"
	"applysmith/internal/llm"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// Test values are assembled at run time so no contact detail sits in the source.
func goodEmail() string { return "riley.tester" + "@" + "mailbox-test.dev" }
func goodPhone() string { return "+" + "1415" + "5552671" }

func bg() context.Context { return context.Background() }

// setupEnv is an initialised workspace with the database open for writing and
// the machine probes replaced.
func setupEnv(t *testing.T) *Env {
	t.Helper()
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	env := &Env{Workspace: ws, Backend: vault.NewMemory(), HomeDir: t.TempDir(), ConfigDir: t.TempDir(),
		Getenv: func(string) string { return "" },
		Chrome: func(context.Context) (browser.Chrome, error) {
			return browser.Chrome{Version: "155.0.1", Major: 155}, nil
		},
		DiskFree: func(string) (uint64, error) { return 50 << 30, nil }}
	closeFn, err := env.Open(bg(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	return env
}

func apply(t *testing.T, env *Env, step string, body string, dry bool) (Applied, error) {
	t.Helper()
	return Apply(bg(), env, step, []byte(body), dry)
}

func mustApply(t *testing.T, env *Env, step, body string) Applied {
	t.Helper()
	res, err := apply(t, env, step, body, false)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	return res
}

func identityBody(name string) string {
	b, _ := json.Marshal(map[string]string{"full_name": name, "email": goodEmail(), "phone": goodPhone(), "city": "Leeds",
		"region": "West Yorkshire", "country": "GB", "street_address": askMe, "postal_code": askMe})
	return string(b)
}

func status(t *testing.T, env *Env, id string) Detection {
	t.Helper()
	return Evaluate(bg(), env, mustStep(id))
}

func hasReason(d Detection, sub string) bool {
	for _, r := range d.Reasons {
		if strings.Contains(r.Text, sub) {
			return true
		}
	}
	return false
}

func TestCatalogHoldsEighteenStepsInOrderAndValidates(t *testing.T) {
	cat := Catalog()
	if len(cat) != 18 {
		t.Fatalf("%d steps", len(cat))
	}
	required := 0
	for i, s := range cat {
		if s.N != i {
			t.Errorf("step %s is %d at %d", s.ID, s.N, i)
		}
		if s.Required {
			required++
		}
	}
	if required != RequiredSteps {
		t.Errorf("%d required steps, progress divides by %d", required, RequiredSteps)
	}
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
}

func TestEveryLaneListsTheCatalogSteps(t *testing.T) {
	env := setupEnv(t)
	rep := State(bg(), env)
	cat := Catalog()
	if len(rep.Steps) != len(cat) {
		t.Fatalf("status lists %d steps", len(rep.Steps))
	}
	for i, v := range rep.Steps {
		if v.ID != cat[i].ID {
			t.Errorf("position %d: %s, want %s", i, v.ID, cat[i].ID)
		}
	}
	if _, err := Schema("frobnicate"); err == nil {
		t.Error("a step outside the catalog was accepted")
	}
}

func TestACatalogEntryCannotBeAddedHalfWired(t *testing.T) {
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	for name, mutate := range map[string]func(*Step){
		"commands": func(s *Step) { s.Commands = nil },
		"guide":    func(s *Step) { s.Guide = "" },
		"schema":   func(s *Step) { s.Schema = nil },
		"purpose":  func(s *Step) { s.Purpose = "" },
		"writer":   func(s *Step) { s.Apply = nil },
	} {
		catalog = append([]Step(nil), saved...)
		mutate(&catalog[3])
		err := ValidateCatalog()
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBlankRequiredFieldIsReportedByName(t *testing.T) {
	env := setupEnv(t)
	d := status(t, env, "identity")
	if d.Status != StepPartial || !hasReason(d, "email is empty") || !hasField(d, "candidate.email") {
		t.Fatalf("identity = %+v", d)
	}
	// The text names the field in words; the configuration path travels in
	// the reason's field, never in the sentence a person reads.
	for _, r := range d.Reasons {
		if strings.Contains(r.Text, "candidate.") {
			t.Errorf("reason %q shows a configuration path", r.Text)
		}
	}
}

func hasField(d Detection, field string) bool {
	for _, r := range d.Reasons {
		if r.Field == field {
			return true
		}
	}
	return false
}

func TestExampleValuesMakeAStepTemplate(t *testing.T) {
	env := setupEnv(t)
	good := identityBody("Riley Tester")
	mustApply(t, env, "identity", good)
	if d := status(t, env, "identity"); d.Status != StepComplete {
		t.Fatalf("identity = %+v", d)
	}
	cases := map[string]struct {
		edit   workspace.Op
		reason string
	}{
		"persona name":   {setOp("candidate.full_name", "Alex Example"), "example name"},
		"reserved email": {setOp("candidate.email", "alex@"+"example.com"), "reserved domain"},
		"fictional":      {setOp("candidate.phone", "+1 555 0100"), "fictional"},
		"placeholder":    {setOp("candidate.address.city", "[Your City]"), "placeholder"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := setupEnv(t)
			mustApply(t, env, "identity", good)
			if _, err := editProfile(env, []workspace.Op{c.edit}, false); err != nil {
				t.Fatal(err)
			}
			d := status(t, env, "identity")
			if d.Status != StepTemplate || !hasReason(d, c.reason) {
				t.Fatalf("identity = %+v", d)
			}
		})
	}
}

func TestExampleEmailAndBadPhoneAreRejectedByTheWriter(t *testing.T) {
	env := setupEnv(t)
	_, err := apply(t, env, "identity", `{"email":"someone@`+`example.org","phone":"12"}`, false)
	fe, ok := err.(FieldErrors)
	if !ok || fe["email"] == "" || fe["phone"] == "" {
		t.Fatalf("err = %v", err)
	}
	// A country is read by its name too, and a national number then by that
	// country's rules; a name the table does not know is the person's to fix.
	mustApply(t, env, "identity", `{"country":"United States","phone":"(415) 555-0142"}`)
	p, _, _ := profileData(env)
	if p.Candidate.Address.Country != "US" || p.Candidate.Phone != "+14155550142" {
		t.Fatalf("country %q phone %q", p.Candidate.Address.Country, p.Candidate.Phone)
	}
	if _, err := apply(t, env, "identity", `{"country":"Atlantis"}`, false); err == nil || !strings.Contains(err.Error(), "two-letter code") {
		t.Fatalf("unknown country err = %v", err)
	}
	mustApply(t, env, "work-authorization", `{"authorized_countries":["usa","U.K.","eu"]}`)
	if p, _, _ = profileData(env); !slices.Equal(p.WorkAuthorization.AuthorizedCountries, []string{"US", "GB", "EU"}) {
		t.Fatalf("authorized = %v", p.WorkAuthorization.AuthorizedCountries)
	}
	// Every citizenship of a dual national is kept, as two-letter codes.
	mustApply(t, env, "work-authorization", `{"citizenships":["China","ca"]}`)
	if p, _, _ = profileData(env); !slices.Equal(p.WorkAuthorization.Citizenships, []string{"CN", "CA"}) {
		t.Fatalf("citizenships = %v", p.WorkAuthorization.Citizenships)
	}
	if _, err := apply(t, env, "work-authorization", `{"citizenships":["Atlantis"]}`, false); err == nil || !strings.Contains(err.Error(), "two-letter code") {
		t.Fatalf("unknown citizenship err = %v", err)
	}
}

// The terminal identity questions offer cv.md's reading for an empty field
// and never put it over a stored value: Enter on every question keeps the
// stored email and stores the CV's name, phone and place.
func TestIdentityQuestionsOfferTheCVForEmptyFieldsOnly(t *testing.T) {
	env := setupEnv(t)
	mustApply(t, env, "identity", `{"email":"`+goodEmail()+`"}`)
	cv := "# Riley Sample\n\nLeeds, West Yorkshire | riley.cv" + "@" + "mailbox-test.dev | " + goodPhone() + "\n\n## Experience\n"
	if err := os.WriteFile(env.Workspace.Path(workspace.FileCV), []byte(cv), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	f := &Flow{Env: env, In: NewLinePrompter(strings.NewReader(strings.Repeat("\n", 8)+"no\n"), &out), Out: &out}
	if err := f.runStep(bg(), mustStep("identity")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	c := func() workspace.Profile { p, _, _ := profileData(env); return p }().Candidate
	if c.Email != goodEmail() || c.FullName != "Riley Sample" || c.Phone != goodPhone() || c.Address.City != "Leeds" || c.Address.Region != "West Yorkshire" || c.Address.Line1 != "" {
		t.Fatalf("%+v\n%s", c, out.String())
	}
}

func TestAckRules(t *testing.T) {
	env := setupEnv(t)
	if err := Ack(bg(), env, "cv"); err == nil {
		t.Fatal("seed text was acknowledged")
	}
	mustApply(t, env, "identity", identityBody("Alex Example"))
	if err := Ack(bg(), env, "identity"); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "identity"); d.Status != StepComplete {
		t.Fatalf("after ack %+v", d)
	}
	// A reserved-domain email can never be acknowledged.
	if _, err := editProfile(env, []workspace.Op{setOp("candidate.email", "x@"+"example.net")}, false); err != nil {
		t.Fatal(err)
	}
	if err := Ack(bg(), env, "identity"); err == nil {
		t.Fatal("a reserved email was acknowledged")
	}
	if err := Ack(bg(), env, "prerequisites"); err == nil {
		t.Fatal("a step without acknowledgements accepted one")
	}
}

func TestSkipIsForOptionalStepsOnly(t *testing.T) {
	env := setupEnv(t)
	if err := Skip(bg(), env, "identity"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v", err)
	}
	if err := Skip(bg(), env, "gmail"); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "gmail"); d.Status != StepSkipped {
		t.Fatalf("gmail = %+v", d)
	}
	if err := Unskip(bg(), env, "gmail"); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "gmail"); d.Status == StepSkipped {
		t.Fatal("unskip did not restore the step")
	}
}

func TestDetectionDoesNotWrite(t *testing.T) {
	env := setupEnv(t)
	mustApply(t, env, "identity", identityBody("Riley Tester"))
	snap := func() map[string]time.Time {
		out := map[string]time.Time{}
		_ = filepath.WalkDir(env.Workspace.Root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.Contains(p, ".applysmith") {
				return nil
			}
			info, _ := d.Info()
			out[p] = info.ModTime()
			return nil
		})
		return out
	}
	rows := func() int {
		var n int
		_ = env.Store.Read().QueryRow(`SELECT count(*) FROM workspace_meta`).Scan(&n)
		return n
	}
	before, nRows := snap(), rows()
	time.Sleep(20 * time.Millisecond)
	State(bg(), env)
	State(bg(), env)
	after := snap()
	for p, mt := range before {
		if !after[p].Equal(mt) {
			t.Errorf("%s was modified by detection", p)
		}
	}
	if len(after) != len(before) || rows() != nRows {
		t.Errorf("files %d->%d, rows %d->%d", len(before), len(after), nRows, rows())
	}
}

func TestPrerequisiteProbes(t *testing.T) {
	env := setupEnv(t)
	env.Chrome = func(context.Context) (browser.Chrome, error) {
		return browser.Chrome{}, &browser.Error{Code: browser.CodeChromeTooOld, Message: "Google Chrome 118 found at /c, version 120 or newer is required; update Google Chrome"}
	}
	d := status(t, env, "prerequisites")
	if d.Status != StepPartial || !hasReason(d, "118") || !hasReason(d, "120") || !hasReason(d, "applysmith doctor --only prerequisites") {
		t.Fatalf("old chrome: %+v", d)
	}
	env.Chrome = setupEnv(t).Chrome
	env.DiskFree = func(string) (uint64, error) { return 3 << 30, nil }
	if d := status(t, env, "prerequisites"); d.Status != StepComplete || len(d.Warnings) == 0 {
		t.Fatalf("3 GiB: %+v", d)
	}
	if r := run(t, env, "prerequisites.disk"); r.Status != Warn {
		t.Fatalf("disk leaf at 3 GiB: %+v", r)
	}
	env.DiskFree = func(string) (uint64, error) { return 1 << 30, nil }
	if d := status(t, env, "prerequisites"); d.Status != StepPartial || !hasReason(d, "disk") {
		t.Fatalf("1 GiB: %+v", d)
	}
}

func TestWorkspaceStepKeepsExistingFilesAndRefusesICloud(t *testing.T) {
	env := setupEnv(t)
	res := mustApply(t, env, "workspace", `{}`)
	found := false
	for _, u := range res.Untouched {
		found = found || u == "cv.md"
	}
	if !found {
		t.Fatalf("untouched = %v", res.Untouched)
	}
	if d := status(t, env, "workspace"); d.Status != StepComplete {
		t.Fatalf("workspace = %+v", d)
	}
	if _, err := os.Stat(env.Workspace.Path(workspace.FileAnswerBank)); err != nil {
		t.Fatalf("the workspace step leaves no default answer bank: %v", err)
	}
	icloud := filepath.Join(env.HomeDir, "Library", "Mobile Documents", "com~apple~CloudDocs", "Applysmith")
	_, err := apply(t, env, "workspace", `{"path":"`+icloud+`"}`, false)
	fe, ok := err.(FieldErrors)
	if !ok || !strings.Contains(fe["path"], "iCloud") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(icloud); statErr == nil {
		t.Fatal("the iCloud folder was created")
	}
	cfg := filepath.Join(env.ConfigDir, "applysmith", "workspace.json")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("the location was not recorded in the injected config dir: %v", err)
	}
	// Creating the files of another workspace (a server started with
	// --workspace on a copy) never moves the default away from a workspace
	// that still exists; a default that is gone is replaced.
	other := setupEnv(t)
	other.ConfigDir = env.ConfigDir
	res, err = Apply(bg(), other, "workspace", []byte(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := workspace.RecordedChoice(env.ConfigDir); got != env.Workspace.Root || !strings.Contains(strings.Join(res.Notes, " "), "did not become the default") {
		t.Fatalf("default = %q, notes %v", got, res.Notes)
	}
	if err := os.RemoveAll(env.Workspace.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(bg(), other, "workspace", []byte(`{}`), false); err != nil {
		t.Fatal(err)
	}
	if got, _ := workspace.RecordedChoice(env.ConfigDir); got != other.Workspace.Root {
		t.Fatalf("a gone default was kept: %q", got)
	}
}

func TestCVStepReportsPlaceholdersAndHoldsHistoryUntilConfirmed(t *testing.T) {
	env := setupEnv(t)
	long := strings.Repeat("Platform engineering across data pipelines and tooling for several teams. ", 6)
	body, _ := json.Marshal(map[string]string{"markdown": "# [Your Name]\n\n## Experience\n\n### Engineer, Northwind Systems (2019-03 to present)\n\n" + long})
	mustApply(t, env, "cv", string(body))
	d := status(t, env, "cv")
	if d.Status != StepTemplate || !hasReason(d, "[Your Name]") {
		t.Fatalf("cv = %+v", d)
	}
	body, _ = json.Marshal(map[string]string{"markdown": "# Riley Tester\n\n## Experience\n\n### Engineer, Northwind Systems (2019-03 to present)\n\n" + long})
	res := mustApply(t, env, "cv", string(body))
	if res.Draft == nil || len(res.Draft.Employment) != 1 {
		t.Fatalf("draft = %+v", res.Draft)
	}
	d = status(t, env, "cv")
	if d.Status != StepPartial || !hasReason(d, "awaits review") {
		t.Fatalf("cv = %+v", d)
	}
	p, _, _ := profileData(env)
	if len(p.History.Employment) != 0 {
		t.Fatal("extracted history was written before confirmation")
	}
	mustApply(t, env, "cv", `{"employment":[{"employer":"Northwind Systems","title":"Engineer","start":"2019-03","end":"present"}]}`)
	if d := status(t, env, "cv"); d.Status != StepComplete {
		t.Fatalf("cv after confirmation = %+v", d)
	}
	if p, _, _ := profileData(env); len(p.History.Employment) != 1 {
		t.Fatalf("history = %+v", p.History)
	}
}

func TestCVWithoutADatedEntryIsNotComplete(t *testing.T) {
	env := setupEnv(t)
	body, _ := json.Marshal(map[string]string{"markdown": "# Riley Tester\n\n" + strings.Repeat("I write software and I like it a lot, in many languages and styles. ", 8)})
	mustApply(t, env, "cv", string(body))
	if d := status(t, env, "cv"); d.Status != StepPartial || !hasReason(d, "no dated") {
		t.Fatalf("cv = %+v", d)
	}
}

// Text edited from an older cv.md must not replace a newer one saved elsewhere.
func TestCVTextFromAnOlderRevisionIsNotSaved(t *testing.T) {
	env := setupEnv(t)
	old, err := env.Workspace.Read(workspace.FileCV)
	if err != nil {
		t.Fatal(err)
	}
	newer := "# Saved in another window\n\n" + strings.Repeat("Platform work. ", 30)
	if _, err := env.Workspace.Write(workspace.FileCV, []byte(newer), old.Revision); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"markdown": "# Edited here\n", "revision": old.Revision})
	if _, err := apply(t, env, "cv", string(body), false); !errors.As(err, new(*StaleError)) {
		t.Fatalf("err = %v", err)
	}
	if c, _ := env.Workspace.Read(workspace.FileCV); string(c.Data) != newer {
		t.Fatalf("cv.md = %q", c.Data)
	}
	body, _ = json.Marshal(map[string]string{"markdown": "# Edited here\n", "revision": workspace.Revision([]byte(newer))})
	mustApply(t, env, "cv", string(body))
}

func TestSponsorshipIsNeverGuessed(t *testing.T) {
	env := setupEnv(t)
	mustApply(t, env, "work-authorization", `{"authorization_status":"citizen","authorized_countries":["gb"],"security_clearance":"none","relocation_default":"open","work_modes":["remote"]}`)
	d := status(t, env, "work-authorization")
	if d.Status != StepPartial || !hasField(d, "work_authorization.requires_sponsorship_now") || !hasField(d, "work_authorization.requires_sponsorship_future") || !hasReason(d, "sponsorship needed now is unanswered") || len(d.Reasons) != 2 {
		t.Fatalf("work-authorization = %+v", d)
	}
	mustApply(t, env, "work-authorization", `{"requires_sponsorship_now":false,"requires_sponsorship_future":true}`)
	if d := status(t, env, "work-authorization"); d.Status != StepComplete {
		t.Fatalf("after answers = %+v", d)
	}
	_, err := apply(t, env, "work-authorization", `{"authorization_status":"work_visa"}`, false)
	if fe, ok := err.(FieldErrors); !ok || fe["status_detail"] == "" {
		t.Fatalf("err = %v", err)
	}
	// A real-world wording that names one status, as the original product
	// stored it, is an answer; a wording that names none is not.
	path := env.Workspace.Path(workspace.FileProfile)
	setVisa := func(v string) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(?m)^(\s*visa_status:).*$`)
		if err := os.WriteFile(path, re.ReplaceAll(b, []byte("${1} "+v)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"F-1 OPT", "H-1B", "Green card holder", "U.S. citizen"} {
		setVisa(v)
		if d := status(t, env, "work-authorization"); d.Status != StepComplete {
			t.Fatalf("%q = %+v", v, d)
		}
	}
	setVisa("Martian pass")
	if d := status(t, env, "work-authorization"); d.Status == StepComplete || !hasReason(d, "names no status") {
		t.Fatalf("unknown wording = %+v", d)
	}
}

func TestTargetsRejectInvertedCompensationAndNeedASkipListReview(t *testing.T) {
	env := setupEnv(t)
	_, err := apply(t, env, "targets", `{"compensation":{"currency":"USD","period":"year","minimum":200000,"target":150000}}`, false)
	fe, ok := err.(FieldErrors)
	if !ok || fe["compensation.minimum"] != "must not be greater than the target" {
		t.Fatalf("err = %v", err)
	}
	body := `{"roles":[{"title":"Backend Engineer","seniority":"senior"}],"locations":["anywhere or remote"],"compensation":{"currency":"usd","period":"year","minimum":100000,"target":150000}}`
	mustApply(t, env, "targets", body)
	d := status(t, env, "targets")
	if d.Status != StepPartial || !hasReason(d, "skip list") {
		t.Fatalf("targets = %+v", d)
	}
	if err := Ack(bg(), env, "targets"); err != nil {
		t.Fatal(err)
	}
	if d := status(t, env, "targets"); d.Status != StepComplete {
		t.Fatalf("after confirming = %+v", d)
	}
	// Editing the list is a new list: the confirmation no longer applies, and an
	// explicit list counts as reviewed on its own.
	mustApply(t, env, "targets", `{"batch_skip_companies":["Acme"]}`)
	if d := status(t, env, "targets"); d.Status != StepComplete {
		t.Fatalf("edited list = %+v", d)
	}
}

func mapKeys(m FieldErrors) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLLMStepOfflineAndConfigurationChange(t *testing.T) {
	env := setupEnv(t)
	if d := status(t, env, "llm"); d.Status != StepMissing {
		t.Fatalf("llm = %+v", d)
	}
	res := mustApply(t, env, "llm", `{"offline":true}`)
	if res.Detection.Status != StepComplete {
		t.Fatalf("llm = %+v", res.Detection)
	}
	if _, err := apply(t, env, "llm", `{"cli":"codex"}`, false); err == nil || !strings.Contains(err.Error(), "acknowledge_cli_limits") {
		t.Fatalf("cli without the acknowledgement: %v", err)
	}
	if _, err := apply(t, env, "llm", `{"preset":"openrouter","offline":true}`, false); err == nil {
		t.Fatal("two choices were accepted")
	}
	if _, err := apply(t, env, "llm", `{"offline":true,"fallback":"deepseek"}`, false); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("a fallback that offline mode would drop was accepted: %v", err)
	}
	if _, err := apply(t, env, "llm", `{"preset":"nvidia"}`, false); err == nil {
		t.Fatal("a preset that setup does not offer was accepted")
	}
	// Every one-key preset the wizard offers is taken, up to its missing key.
	for _, p := range llm.SetupPresets() {
		if p.Kind == llm.KindCLI {
			continue
		}
		_, err := apply(t, env, "llm", `{"preset":"`+p.Name+`"}`, false)
		if _, ok := err.(*HumanError); !ok {
			t.Fatalf("%s: no key must need a person, got %v", p.Name, err)
		}
	}
}

func TestSecretsAreShownByPresenceOnly(t *testing.T) {
	env := setupEnv(t)
	key := "sk-" + strings.Repeat("q", 30)
	if err := SaveProviderKey(bg(), env, "deepseek", "short"); err == nil {
		t.Fatal("a short key was stored")
	}
	if err := SaveProviderKey(bg(), env, "deepseek", key); err != nil {
		t.Fatal(err)
	}
	if err := env.Router.ConfigurePreset(bg(), "deepseek", llm.ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	rep := State(bg(), env)
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), key) {
		t.Fatal("the key is in the status output")
	}
	if len(rep.Secrets) != 1 || !rep.Secrets[0].Present || rep.Secrets[0].SavedAt == "" {
		t.Fatalf("secrets = %+v", rep.Secrets)
	}
}

func TestCVStepWritesNothingWhenTheMarkdownIsRefused(t *testing.T) {
	env := setupEnv(t)
	mustApply(t, env, "identity", identityBody("Riley Tester"))
	before, _ := env.Workspace.Read(workspace.FileProfile)
	big := strings.Repeat("Experience line padding text. ", 10000) // over the document size limit
	body, _ := json.Marshal(map[string]any{"markdown": big,
		"employment": []map[string]string{{"employer": "Acme Ltd", "title": "Engineer", "start": "2020-01", "end": "present"}}})
	_, err := apply(t, env, "cv", string(body), false)
	if err == nil {
		t.Fatal("an oversized cv.md was accepted")
	}
	after, _ := env.Workspace.Read(workspace.FileProfile)
	if after.Revision != before.Revision {
		t.Fatal("profile.yml was edited although cv.md was refused")
	}
}

func TestWorkspaceStepSeedsTheDefaultBankOnceAndKeepsAnExistingOne(t *testing.T) {
	env := setupEnv(t)
	path := env.Workspace.Path(workspace.FileAnswerBank)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	res := mustApply(t, env, "workspace", `{}`)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(answerbank.DefaultBytes()) {
		t.Fatalf("a fresh workspace must get the embedded default bank: %v", err)
	}
	if slices.Contains(res.Untouched, "answer-bank.yml") {
		t.Fatalf("a bank the step created is not untouched: %v", res.Untouched)
	}
	if d := status(t, env, "workspace"); d.Status != StepComplete {
		t.Fatalf("workspace = %+v", d)
	}
	mine := "version: 1\nentries: []\n# mine\n"
	if err := os.WriteFile(path, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	res = mustApply(t, env, "workspace", `{}`)
	if kept, _ := os.ReadFile(path); string(kept) != mine || !slices.Contains(res.Untouched, "answer-bank.yml") {
		t.Fatalf("an existing bank was changed or not reported: %q %v", kept, res.Untouched)
	}
	dry, err := apply(t, env, "workspace", `{}`, true)
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(path); string(kept) != mine || !dry.DryRun {
		t.Fatalf("a dry run must write nothing: %q", kept)
	}
}

// A ChatGPT plan has no API key to ask for: setup and the doctor ask for the
// sign-in instead.
func TestChatGPTPresetAsksForTheSignInNotAKey(t *testing.T) {
	env := setupEnv(t)
	pr, _ := llm.PresetByName("chatgpt")
	err := llm.UpdateSettings(bg(), env.Store, time.Now(), func(s *llm.Settings) error {
		s.Providers, s.Preset = append(s.Providers, pr.ProviderConfig()), "chatgpt"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d := status(t, env, "llm")
	if !hasReason(d, "not signed in to ChatGPT") || hasReason(d, "API key") {
		t.Fatalf("reasons = %+v", d.Reasons)
	}
	if out := checkLLMProvider(bg(), env); out.Status != Fail || !strings.Contains(out.Summary, "not signed in to ChatGPT") {
		t.Fatalf("doctor = %+v", out)
	}
	if got := LLMSecrets(bg(), env); len(got) != 0 {
		t.Fatalf("secrets = %+v: a ChatGPT plan has no key", got)
	}
}

// A mistake the questions catch (a compensation that is not a number) asks
// the step again, as one the writer catches does, instead of ending setup.
func TestAMistakeCaughtWhileAskingIsAskedAgain(t *testing.T) {
	env := setupEnv(t)
	start := "Platform Engineer\nsenior\nprimary\nno\nanywhere or remote\nUSD\nyear\n"
	var out strings.Builder
	f := &Flow{Env: env, In: NewLinePrompter(strings.NewReader(start+"lots\n"+start+"150000\n200000\nyes\n"), &out), Out: &out}
	if err := f.runStep(bg(), mustStep("targets")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "compensation.minimum: must be a positive whole number") {
		t.Fatalf("the mistake was not named:\n%s", out.String())
	}
}

// ValidateCatalog checks the catalog itself: ids unique and numbered 0..17 in order,
// and every available step carries the full contract. It names the missing
// piece so a half-wired step fails a test.
func ValidateCatalog() error {
	seen := map[string]bool{}
	for i, s := range catalog {
		switch {
		case s.N != i:
			return fmt.Errorf("step %q is numbered %d at position %d", s.ID, s.N, i)
		case seen[s.ID]:
			return fmt.Errorf("step id %q is listed twice", s.ID)
		case s.Title == "" || s.Purpose == "" || s.Storage == "" || s.Phase == "":
			return fmt.Errorf("step %q lacks a title, purpose, phase or storage description", s.ID)
		case !s.Required && s.SkipCost == "":
			return fmt.Errorf("optional step %q states no skip consequence", s.ID)
		}
		seen[s.ID] = true
		switch {
		case s.Detect == nil:
			return fmt.Errorf("step %q has no completion detector", s.ID)
		case s.Apply == nil:
			return fmt.Errorf("step %q has no writer", s.ID)
		case len(s.Commands) == 0:
			return fmt.Errorf("step %q names no command that completes it", s.ID)
		case s.Guide == "":
			return fmt.Errorf("step %q names no guide page", s.ID)
		case len(s.Schema) == 0 && !s.Inputless:
			return fmt.Errorf("step %q declares no input schema", s.ID)
		case !hasCheck(s.ID):
			return fmt.Errorf("step %q has no doctor check with its id", s.ID)
		}
	}
	return nil
}

func hasCheck(group string) bool {
	for _, c := range Checks() {
		if c.Group() == group {
			return true
		}
	}
	return false
}

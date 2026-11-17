package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
)

const holdURL = "https://careers.example.test/jobs/81"

func holdCand() Candidate { return lsCand(holdURL, "Example Corp", "Data Engineer") }

func holdRule(res SelectResult) string {
	if len(res.Skipped) == 1 {
		return res.Skipped[0].Rule
	}
	return ""
}

func TestApplyList_Holds_ReadWholeLedgerAndRetryTodayDoesNotLiftFacts(t *testing.T) {
	old := t0.Add(-40 * 24 * time.Hour)
	closed := lsRun("r1", holdURL, old, func(r *RunFact) {
		r.Class, r.Stage, r.Reason, r.Build = ClassPosting, StageAccount, "posting closed: HTTP 404", "build-a"
	})
	// A precheck stop of an earlier version classed a bot wall as a defect.
	wall := lsRun("r2", holdURL, old, func(r *RunFact) {
		r.Class, r.Stage, r.Reason = ClassDefect, StageResolve, "bot-challenge: just a moment; complete verification yourself"
	})
	for name, run := range map[string]RunFact{"a closed posting": closed, "a bot challenge": wall} {
		t.Run(name+" holds across days and retry-today", func(t *testing.T) {
			f := Facts{Runs: []RunFact{run}}
			if res := lsSelect(t, f, []Candidate{holdCand()}, nil); holdRule(res) != RuleHold {
				t.Fatalf("%+v", res)
			}
			if res := lsSelect(t, f, []Candidate{holdCand()}, func(o *SelectOptions) { o.RetryToday = true }); holdRule(res) != RuleHold {
				t.Fatalf("retry-today lifted a fact: %+v", res)
			}
			res := lsSelect(t, f, []Candidate{holdCand()}, func(o *SelectOptions) { o.NamedCompanies = []string{"Example Corp"} })
			if len(res.Rows) != 1 {
				t.Fatalf("naming the company after rechecking runs it: %+v", res)
			}
		})
	}
	t.Run("a later run of the posting is the verdict", func(t *testing.T) {
		later := lsRun("r3", holdURL, old.Add(24*time.Hour), func(r *RunFact) { r.Kind, r.Class, r.Stage, r.Reason = OutcomeFilled, "", "", "" })
		if res := lsSelect(t, Facts{Runs: []RunFact{closed, later}}, []Candidate{holdCand()}, nil); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a hold never lifts a duplicate or the skip list", func(t *testing.T) {
		res := lsSelect(t, Facts{Runs: []RunFact{closed}}, []Candidate{holdCand()}, func(o *SelectOptions) {
			o.NamedCompanies, o.SkipList = []string{"Example Corp"}, []string{"Example Corp"}
		})
		if holdRule(res) != RuleSkipList {
			t.Fatalf("%+v", res)
		}
	})
}

func TestApplyList_Holds_JudgementExpiresOnNewCleanBuild(t *testing.T) {
	yesterday := t0.Add(-30 * time.Hour)
	judged := func(build string) RunFact {
		return lsRun("r1", holdURL, yesterday, func(r *RunFact) {
			r.Class, r.Stage, r.Reason, r.Build = ClassPosting, StageAccount, "the walk found no form or Submit control", build
		})
	}
	cases := []struct {
		name         string
		run          RunFact
		current      string
		held         bool
		heldMessages string
	}{
		{"the same clean build keeps its own judgement", judged("build-a"), "build-a", true, "terminal posting verdict"},
		{"a new clean build retries it without being named", judged("build-a"), "build-b", false, ""},
		{"a build with local modifications never vouches", judged("build-a-dirty"), "build-a-dirty", false, ""},
		{"nor does a record that cannot name its build", judged("unknown"), "unknown", false, ""},
		{"a fact about the posting holds on any build", lsRun("r1", holdURL, yesterday, func(r *RunFact) {
			r.Class, r.Reason, r.Build = ClassPosting, "liveness expired: no longer accepting applications", "build-a"
		}), "build-z", true, "liveness expired"},
		{"yesterday's answer gap or defect holds nothing", lsRun("r1", holdURL, yesterday, func(r *RunFact) { r.Class, r.Build = ClassDefect, "build-a" }), "build-a", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, Facts{Runs: []RunFact{c.run}}, []Candidate{holdCand()}, func(o *SelectOptions) { o.Build = c.current })
			if held := holdRule(res) == RuleHold; held != c.held {
				t.Fatalf("held=%v, want %v: %+v", held, c.held, res)
			}
			if c.held && !strings.Contains(res.Skipped[0].Reason, c.heldMessages) {
				t.Fatalf("reason %q lacks %q", res.Skipped[0].Reason, c.heldMessages)
			}
		})
	}
}

func TestApplyList_Holds_MirrorChallengeExpiresLikeJudgement(t *testing.T) {
	mirror := "https://jobright.ai/jobs/info/905"
	yesterday := t0.Add(-30 * time.Hour)
	wall := func(mod func(*RunFact)) RunFact {
		return lsRun("r1", mirror, yesterday, func(r *RunFact) {
			r.Class, r.Stage, r.Reason, r.Build, r.ATS = ClassDefect, StageAccount, "bot check: a challenge stands where the page should be", "build-a", ATSGeneric
			r.HasResolve, r.ResolveDetail = true, "no employer posting was found behind the mirror"
			if mod != nil {
				mod(r)
			}
		})
	}
	row := lsCand(mirror, "Example Corp", "Data Engineer")
	cases := []struct {
		name    string
		run     RunFact
		current string
		held    bool
	}{
		{"a walk met the mirror's wall on the same clean build", wall(nil), "build-a", true},
		{"current code never loads the mirror, so it expires", wall(nil), "build-b", false},
		{"a modified build never vouches either way", wall(func(r *RunFact) { r.Build = "build-a-dirty" }), "build-a-dirty", false},
		{"a wall met after the walk moved to the employer's ATS holds", wall(func(r *RunFact) { r.ResolveDetail = "moved off an aggregator posting to the employer" }), "build-b", true},
		{"a run that never walked (a list pre-check) holds", wall(func(r *RunFact) { r.ATS, r.HasResolve = "", false }), "build-b", true},
		{"a resolve that only classified the mirror holds", wall(func(r *RunFact) { r.HasResolve = false }), "build-b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, Facts{Runs: []RunFact{c.run}}, []Candidate{row}, func(o *SelectOptions) { o.Build = c.current })
			if held := holdRule(res) == RuleHold; held != c.held {
				t.Fatalf("held=%v, want %v: %+v", held, c.held, res)
			}
		})
	}
	t.Run("a challenge on an ATS page keeps its hold across builds", func(t *testing.T) {
		r := wall(func(r *RunFact) { r.URL, r.ATS = holdURL, ATSWorkday })
		res := lsSelect(t, Facts{Runs: []RunFact{r}}, []Candidate{holdCand()}, func(o *SelectOptions) { o.Build = "build-z" })
		if holdRule(res) != RuleHold {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the same wall met today expires the same way", func(t *testing.T) {
		r := wall(func(r *RunFact) { r.Started, r.Finished = t0.Add(-time.Hour), t0.Add(-58*time.Minute) })
		res := lsSelect(t, Facts{Runs: []RunFact{r}}, []Candidate{row}, func(o *SelectOptions) { o.Build = "build-b"; o.RetryToday = true })
		if len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
}

func userDataRun(id string, started time.Time, unresolved ...string) RunFact {
	return lsRun(id, holdURL, started, func(r *RunFact) {
		r.Pages = []PageReport{{URL: holdURL, Unresolved: unresolved}}
		r.Build = "build-a"
	})
}

func TestApplyList_Holds_UserDataStopWaitsForSaveOrNaming(t *testing.T) {
	stoppedAt := t0.Add(-3 * time.Hour)
	run := userDataRun("r1", stoppedAt, "What forms of deferred compensation do you have? (declined:No data)", "Video introduction (declined:no video)")
	if !userDataStop(run) {
		t.Fatal("every leftover is the user's own missing data")
	}
	onNew := func(mod func(*SelectOptions)) func(*SelectOptions) {
		return func(o *SelectOptions) {
			o.Build = "build-b"
			if mod != nil {
				mod(o)
			}
		}
	}
	t.Run("a deploy does not move it", func(t *testing.T) {
		res := lsSelect(t, Facts{Runs: []RunFact{run}}, []Candidate{holdCand()}, onNew(nil))
		if holdRule(res) != RuleToday || !strings.Contains(res.Skipped[0].Reason, "waits for your answer") {
			t.Fatalf("%+v", res)
		}
		res = lsSelect(t, Facts{Runs: []RunFact{run}}, []Candidate{holdCand()}, onNew(func(o *SelectOptions) { o.RetryToday = true }))
		if holdRule(res) != RuleToday {
			t.Fatalf("retry-today alone does not lift a user-data stop: %+v", res)
		}
	})
	t.Run("a save after the stop lets it run on the new code", func(t *testing.T) {
		res := lsSelect(t, Facts{Runs: []RunFact{run}, DataSavedAt: stoppedAt.Add(time.Hour)}, []Candidate{holdCand()}, onNew(nil))
		if len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
		res = lsSelect(t, Facts{Runs: []RunFact{run}, DataSavedAt: stoppedAt.Add(-time.Hour)}, []Candidate{holdCand()}, onNew(nil))
		if holdRule(res) != RuleToday {
			t.Fatalf("a save before the stop has not seen the stop: %+v", res)
		}
	})
	t.Run("naming the company with retry-today runs it", func(t *testing.T) {
		res := lsSelect(t, Facts{Runs: []RunFact{run}}, []Candidate{holdCand()}, onNew(func(o *SelectOptions) {
			o.RetryToday, o.NamedCompanies = true, []string{"Example Corp"}
		}))
		if len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("one other reason makes it a verdict on the code", func(t *testing.T) {
		code := userDataRun("r1", stoppedAt, "Why us? (declined:No data)", "Country of study (abstained:no-answer)")
		if userDataStop(code) {
			t.Fatal("an abstain is the code's")
		}
		if res := lsSelect(t, Facts{Runs: []RunFact{code}}, []Candidate{holdCand()}, onNew(nil)); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a mismatch, unkept write, flag or failed re-read on any page is not user data", func(t *testing.T) {
		for name, page := range map[string]PageReport{
			"mismatch":  {Mismatches: []string{"Email"}},
			"unkept":    {Uncommitted: []string{"Phone"}},
			"flag":      {Flags: []string{"the review disagrees"}},
			"re-read":   {Notes: []string{"the page re-read failed: connection lost"}},
			"leftovers": {Unresolved: []string{"Why us? (abstained:no-answer)"}},
		} {
			r := userDataRun("r1", stoppedAt, "Why us? (declined:No data)")
			r.Pages = append(r.Pages, page)
			if userDataStop(r) {
				t.Errorf("%s: still read as user data", name)
			}
		}
		other := userDataRun("r1", stoppedAt, "Why us? (declined:No data)")
		other.Class, other.Stage = ClassDefect, StageFill
		if userDataStop(other) {
			t.Error("only an answer gap at check can be user data")
		}
		if userDataStop(lsRun("r1", holdURL, stoppedAt, nil)) {
			t.Error("no leftovers at all is not user data")
		}
	})
}

// The rule reads the reasons the check writes today: a question the answer
// bank holds no entry for, which the grounded rung declined, and one the user
// asked to be asked about, wait for the user; a decline of a question the
// matcher found ambiguous, and a write the page refused, are the code's.
func TestApplyList_Holds_TheChecksOwnLeftoverReasonsAreReadAsUserData(t *testing.T) {
	leftover := func(t *testing.T, model string, row engine.Row) RunFact {
		l := newLadderEnv(t, ladderOwn, script(model, "[]", "[]"), nil)
		ps := l.run(row)
		if len(ps.report.Unresolved) != 1 {
			t.Fatalf("unresolved %v", ps.report.Unresolved)
		}
		return lsRun("r1", holdURL, t0.Add(-3*time.Hour), func(r *RunFact) { r.Pages = []PageReport{ps.report} })
	}
	const q, declines = "Which forms of deferred compensation do you hold?", `[{"id":1,"decline":true}]`
	askMe, ambiguous := abstained("#1", q, "text"), abstained("#1", q, "text")
	askMe.MatcherReason, ambiguous.MatcherReason = string(match.ReasonAskMe), string(match.ReasonAmbiguous)
	failed := engine.Row{Selector: "#1", Label: q, Required: true, Outcome: "failed", Reason: "write-failed", Kind: "text"}
	for _, c := range []struct {
		name  string
		model string
		row   engine.Row
		want  bool
	}{
		{"no bank entry and the grounded rung declined", declines, abstained("#1", q, "text"), true},
		{"the user asked to be asked", "[]", askMe, true},
		{"an ambiguous match the rung declined", declines, ambiguous, false},
		{"a write the page refused", "[]", failed, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := leftover(t, c.model, c.row)
			if got := userDataStop(r); got != c.want {
				t.Fatalf("%v read as user data %v, want %v", r.Pages[0].Unresolved, got, c.want)
			}
		})
	}
}

func TestApplyList_Holds_ProfileAnsweredLabelNeverUserData(t *testing.T) {
	stoppedAt := t0.Add(-3 * time.Hour)
	profile := []string{"Legal First Name", "Last Name*", "Email Address", "Confirm your email", "Phone Number", "Home Address Line 1", "City", "Postal Code", "Country", "State/Province", "Full name"}
	for _, label := range profile {
		r := userDataRun("r1", stoppedAt, label+" (declined:unknown)")
		if userDataStop(r) {
			t.Errorf("%q is answered by the profile and must be a code verdict", label)
		}
	}
	for _, label := range []string{"LinkedIn URL", "GitHub profile", "Personal website", "School name", "Previous employer"} {
		r := userDataRun("r1", stoppedAt, label+" (declined:No data)")
		if !userDataStop(r) {
			t.Errorf("%q can lack data in the profile and stays holdable", label)
		}
	}
	// One profile label among user-data rows makes the whole stop a code verdict.
	if userDataStop(userDataRun("r1", stoppedAt, "Deferred compensation (declined:No data)", "Legal First Name (declined:unknown)")) {
		t.Error("a mislabelled name row hid the fix")
	}
	run := userDataRun("r1", stoppedAt, "Legal First Name (declined:unknown)")
	res := lsSelect(t, Facts{Runs: []RunFact{run}}, []Candidate{holdCand()}, nil)
	if len(res.Rows) != 1 {
		t.Fatalf("the stop re-runs on the next build: %+v", res)
	}
}

func TestApplyList_Cooldown_ExactAccountHostTwentyFourHours(t *testing.T) {
	signin := func(host string, mod func(*SigninRecord)) []SigninRecord {
		s := SigninRecord{Host: host, Refusals: 3, Exhausted: true}
		if mod != nil {
			mod(&s)
		}
		return []SigninRecord{s}
	}
	failed := func(ago time.Duration, url string, signins []SigninRecord, mod func(*RunFact)) RunFact {
		return lsRun("c-"+url, url, t0.Add(-ago), func(r *RunFact) {
			r.Class, r.Stage, r.Reason, r.Build, r.ATS = ClassCredential, StageAccount, "every stored password was refused", "build-a", ATSGeneric
			r.Signins = signins
			if mod != nil {
				mod(r)
			}
		})
	}
	other := lsCand("https://careers.example.test/jobs/other", "Sample Inc", "Analyst")
	beyond := func(ago time.Duration) Facts {
		return Facts{Runs: []RunFact{failed(ago, "https://careers.example.test/jobs/first", signin("careers.example.test", nil), nil)}}
	}
	t.Run("another row on the host is skipped for 24 hours", func(t *testing.T) {
		res := lsSelect(t, beyond(2*time.Hour), []Candidate{other}, nil)
		if holdRule(res) != RuleHold || !strings.Contains(res.Skipped[0].Reason, "credential cooldown for careers.example.test") {
			t.Fatalf("%+v", res)
		}
		if res := lsSelect(t, beyond(25*time.Hour), []Candidate{other}, nil); len(res.Rows) != 1 {
			t.Fatalf("the cooldown ended: %+v", res)
		}
	})
	t.Run("retry-today does not lift it, naming the company does", func(t *testing.T) {
		if res := lsSelect(t, beyond(2*time.Hour), []Candidate{other}, func(o *SelectOptions) { o.RetryToday = true }); holdRule(res) != RuleHold {
			t.Fatalf("%+v", res)
		}
		if res := lsSelect(t, beyond(2*time.Hour), []Candidate{other}, func(o *SelectOptions) { o.NamedCompanies = []string{"Sample Inc"} }); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a new build does not lift it", func(t *testing.T) {
		if res := lsSelect(t, beyond(2*time.Hour), []Candidate{other}, func(o *SelectOptions) { o.Build = "build-z" }); holdRule(res) != RuleHold {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a successful sign-in later clears the host", func(t *testing.T) {
		f := beyond(5 * time.Hour)
		f.Runs = append(f.Runs, lsRun("ok", "https://careers.example.test/jobs/second", t0.Add(-time.Hour), func(r *RunFact) {
			r.Kind, r.Class, r.Stage, r.Reason = OutcomeFilled, "", "", ""
			r.Signins = []SigninRecord{{Host: "careers.example.test", Succeeded: "tenant-key"}}
		}))
		if res := lsSelect(t, f, []Candidate{other}, nil); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the sign-in journal's host is used though the posting was a mirror", func(t *testing.T) {
		f := Facts{Runs: []RunFact{failed(time.Hour, "https://jobright.ai/jobs/info/22", signin("careers.example.test", nil), func(r *RunFact) { r.ATS = ATSGeneric })}}
		if res := lsSelect(t, f, []Candidate{other}, nil); holdRule(res) != RuleHold {
			t.Fatalf("the employer's host is cooled: %+v", res)
		}
		if res := lsSelect(t, f, []Candidate{lsCand("https://jobright.ai/jobs/info/23", "Third Co", "Writer")}, nil); len(res.Rows) != 1 {
			t.Fatalf("the mirror's own host is not the account host: %+v", res)
		}
	})
	t.Run("Workday tenants and wd hosts stay separate", func(t *testing.T) {
		f := Facts{Runs: []RunFact{failed(time.Hour, "https://acme.wd5.myworkdayjobs.com/en-US/External/job/1", signin("acme.wd5.myworkdayjobs.com", nil), func(r *RunFact) { r.ATS = ATSWorkday })}}
		same := lsCand("https://acme.wd5.myworkdayjobs.com/en-US/External/job/2", "Acme Co", "Engineer")
		another := lsCand("https://beta.wd5.myworkdayjobs.com/en-US/External/job/3", "Beta Co", "Engineer")
		wd1 := lsCand("https://acme.wd1.myworkdayjobs.com/en-US/External/job/4", "Acme Intl", "Engineer")
		res := lsSelect(t, f, []Candidate{same, another, wd1}, nil)
		if len(res.Skipped) != 1 || res.Skipped[0].URL != same.URL || len(res.Rows) != 2 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a failure on a shared board with no account-host evidence cools no employer", func(t *testing.T) {
		f := Facts{Runs: []RunFact{failed(time.Hour, "https://boards.greenhouse.io/acme/jobs/1", nil, func(r *RunFact) { r.ATS = "greenhouse" })}}
		if res := lsSelect(t, f, []Candidate{lsCand("https://boards.greenhouse.io/beta/jobs/2", "Beta Co", "Engineer")}, nil); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a stop with a sign-in record that holds only the earlier cooldown does not extend it", func(t *testing.T) {
		f := Facts{Runs: []RunFact{
			failed(30*time.Hour, "https://careers.example.test/jobs/first", signin("careers.example.test", nil), nil),
			failed(time.Hour, "https://careers.example.test/jobs/again", signin("careers.example.test", func(s *SigninRecord) { s.Refusals, s.Exhausted, s.Held = 0, false, true }), nil),
		}}
		if res := lsSelect(t, f, []Candidate{other}, nil); len(res.Rows) != 1 {
			t.Fatalf("a held row made no new refusal: %+v", res)
		}
	})
	t.Run("the employer URL a mirror resolved to is checked against the cooldown", func(t *testing.T) {
		f := beyond(time.Hour)
		sel := NewSelector(lsEvidence{f: f})
		sel.Resolve = func(_ context.Context, c Candidate) (Resolution, error) {
			return Resolution{URL: "https://careers.example.test/jobs/resolved"}, nil
		}
		res, err := sel.Select(context.Background(), []Candidate{lsCand("https://jobright.ai/jobs/info/77", "Fourth Co", "Engineer")}, lsOpts(nil))
		if err != nil || holdRule(res) != RuleHold {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

func TestApplyList_Skip_SignupFailedAccountExistsHeldEnvironmentNot(t *testing.T) {
	exists := lsRun("r1", "https://acme.example.test/jobs/1", t0.Add(-2*time.Hour), func(r *RunFact) {
		r.Class, r.Stage, r.Build, r.ATS = ClassCredential, StageAccount, "build-b", ATSGeneric
		r.Reason = "an account already exists on acme.example.test and the stored password does not match it"
	})
	next := lsCand("https://acme.example.test/jobs/2", "Acme Co", "Writer")
	if res := lsSelect(t, Facts{Runs: []RunFact{exists}}, []Candidate{next}, nil); holdRule(res) != RuleHold {
		t.Fatalf("later rows on the host are held like a credential stop: %+v", res)
	}
	// The same row itself is a credential stop today.
	same := lsCand(exists.URL, "Acme Co", "Engineer")
	if res := lsSelect(t, Facts{Runs: []RunFact{exists}}, []Candidate{same}, func(o *SelectOptions) { o.RetryToday = true }); holdRule(res) != RuleToday {
		t.Fatalf("%+v", res)
	}
	env := exists
	env.Class, env.Stage, env.Reason = ClassEnvironment, StagePreflight, "the browser could not be attached"
	if res := lsSelect(t, Facts{Runs: []RunFact{env}}, []Candidate{next, same}, nil); len(res.Rows) != 2 {
		t.Fatalf("an environment account stop holds nothing: %+v", res)
	}
}

func TestApplyList_Holds_UnparsedLeftoverText(t *testing.T) {
	cases := map[string][2]string{
		"Label (declined:No data)":            {"Label", "declined:No data"},
		"Dates (MM/DD) of study (declined:x)": {"Dates (MM/DD) of study", "declined:x"},
		"No parentheses":                      {"No parentheses", ""},
		"Unbalanced ) (declined:No data)":     {"Unbalanced )", "declined:No data"},
	}
	for in, want := range cases {
		if label, detail := unresolvedParts(in); label != want[0] || detail != want[1] {
			t.Errorf("unresolvedParts(%q) = %q, %q", in, label, detail)
		}
	}
}

// Every posting fact the chain records holds the row, in the words each stop
// writes; only a fact that the posting is closed takes it off every lane.
func TestApplyList_Holds_ClosedPostingFactIsMarkedClosed(t *testing.T) {
	old := t0.Add(-40 * 24 * time.Hour)
	for reason, closed := range map[string]bool{
		"posting closed: HTTP 404": true,
		"posting closed: HTTP 410": true,
		"posting closed: the posting is no longer accepting applications":                                                  true,
		`the posting is gone: LinkedIn answers "Page not found" (404) for https://www.linkedin.com/jobs/view/1`:            true,
		"walk ended posting-closed: the posting address now opens https://careers.example.test/, a page that names no job": true,
		"liveness expired: http-410":                                           true,
		`already applied outside Easy Apply ("Applied 3 days ago")`:            false,
		"walk ended already-applied: an application for this job is on file":   false,
		"walk ended email-application: send your resume to the hiring manager": false,
	} {
		run := lsRun("r1", holdURL, old, func(r *RunFact) { r.Class, r.Stage, r.Reason = ClassPosting, StageFill, reason })
		got, err := NewSelector(lsEvidence{f: Facts{Runs: []RunFact{run}}}).Check(context.Background(), []Candidate{holdCand()}, lsOpts(nil))
		if err != nil || got[0] == nil || got[0].Rule != RuleHold || got[0].Closed != closed {
			t.Errorf("%s: %+v %v, want held with closed = %v", reason, got[0], err, closed)
		}
	}
}

func TestApplyList_Holds_NewDocumentsLiftTodaysStop(t *testing.T) {
	stoppedAt := t0.Add(-3 * time.Hour)
	const row = 7
	cand := holdCand()
	cand.TrackerID = row
	judged := lsRun("r1", holdURL, stoppedAt, func(r *RunFact) {
		r.Reason, r.Build = "Cover letter (declined: the documents floor refused a letter)", "build-b"
	})
	userData := userDataRun("r1", stoppedAt, "Video introduction (declined:no video)")
	credential := lsRun("r1", holdURL, stoppedAt, func(r *RunFact) { r.Class, r.Stage, r.Reason = ClassCredential, StageAccount, "a sign-in is needed" })
	submitted := lsRun("r1", holdURL, stoppedAt, func(r *RunFact) { r.Kind, r.Class, r.Stage, r.Reason = OutcomeSubmitted, "", "", "" })
	cases := []struct {
		name string
		run  RunFact
		held bool
	}{
		{"a judgement of the same clean build", judged, false},
		{"the user's own missing data", userData, false},
		{"a sign-in wall", credential, true},
		{"a submit today", submitted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := Facts{Runs: []RunFact{c.run}, DocumentsAt: map[int64]time.Time{row: stoppedAt.Add(-time.Hour)}}
			if res := lsSelect(t, before, []Candidate{cand}, nil); holdRule(res) != RuleToday {
				t.Fatalf("a document from before the stop lifts nothing: %+v", res)
			}
			after := Facts{Runs: []RunFact{c.run}, DocumentsAt: map[int64]time.Time{row: c.run.Finished.Add(time.Minute)}}
			if held := holdRule(lsSelect(t, after, []Candidate{cand}, nil)) == RuleToday; held != c.held {
				t.Fatalf("held = %v, want %v", held, c.held)
			}
		})
	}
	t.Run("a candidate without its row id is found by its address", func(t *testing.T) {
		f := Facts{Runs: []RunFact{judged}, Tracker: []TrackerFact{{ID: row, Company: "Example Corp", Role: "Data Engineer", Status: "evaluated", Key: postingKey(holdURL)}},
			DocumentsAt: map[int64]time.Time{row: judged.Finished.Add(time.Minute)}}
		if res := lsSelect(t, f, []Candidate{holdCand()}, nil); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
	})
}

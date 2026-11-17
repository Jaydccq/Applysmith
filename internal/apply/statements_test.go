package apply

import (
	"testing"
)

func TestApply_PageState_StatementsNarrowAndQuestionsNeverMatch(t *testing.T) {
	for _, c := range []struct {
		name   string
		lines  []string
		closed bool
		done   bool
		bot    bool
	}{
		{"closed posting", []string{"Example Corp", "This job is no longer available."}, true, false, false},
		{"jobvite listing gone", []string{"NinjaOne Careers", "The job listing no longer exists."}, true, false, false},
		{"does not exist", []string{"This position doesn\u2019t exist anymore."}, true, false, false},
		{"workday requisition gone", []string{"Careers", "The page you are looking for doesn't exist.", "Privacy Policy"}, true, false, false},
		{"filled position", []string{"This position has been filled"}, true, false, false},
		{"no longer accepting", []string{"We are no longer accepting applications for this role."}, true, false, false},
		// Node's posting-closed cases: Chewy's Phenom notice and the other
		// boards' phrasings, and the live-posting lines a looser pattern breaks.
		{"Phenom (Chewy) filled", []string{"Skip to main content", "Search Jobs", "We're sorry\u2026 the job you are trying to apply for has been filled.", "Maybe you would like to consider the Categories below :"}, true, false, false},
		{"applications closed", []string{"Applications for this position are closed."}, true, false, false},
		{"requisition removed", []string{"This requisition has been removed."}, true, false, false},
		{"ashby job not found", []string{"Jobs", "Job not found", "The job you requested was not found.", "View all open positions"}, true, false, false},
		{"closed roles archive", []string{"Browse closed roles in our archive."}, false, false, false},
		{"office closed", []string{"Please note the office is closed on public holidays."}, false, false, false},
		{"filled internally question", []string{"Have you previously applied to a position that was filled internally?"}, false, false, false},
		{"open until filled (iCIMS)", []string{"Actual pay may vary. This job posting will remain open until the position is filled."}, false, false, false},
		{"accepted until the role has been filled", []string{"Applications are accepted until the role has been filled."}, false, false, false},
		{"pay at the level the role is filled (SmartRecruiters)", []string{"Compensation will be based primarily on the job level at which the role is filled and the candidate's experience."}, false, false, false},
		{"closed alone", []string{"Closed"}, false, false, false},
		{"applications close on a date", []string{"Applications close on 30 September"}, false, false, false},
		{"open enrolment", []string{"Open enrolment is closed"}, false, false, false},
		{"question about earlier applications", []string{"Have you previously applied to this company?"}, false, false, false},
		{"question about being closed", []string{"Is this position no longer available?"}, false, false, false},
		{"already applied", []string{"You've already applied to this job."}, false, true, false},
		{"already applied wording in a question", []string{"Have you already applied?"}, false, false, false},
		{"interstitial copy", []string{"Verify you are human"}, false, false, true},
		{"cloudflare title style", []string{"Just a moment..."}, false, false, true},
		{"job description about security", []string{"You will design bot detection and security verification systems for our platform."}, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ClosedNotice(c.lines) != ""; got != c.closed {
				t.Errorf("closed = %v", got)
			}
			if got := AlreadyAppliedNotice(c.lines) != ""; got != c.done {
				t.Errorf("already applied = %v", got)
			}
			if got := BotInterstitial("", c.lines) != ""; got != c.bot {
				t.Errorf("bot = %v", got)
			}
		})
	}
}

func TestApply_Walk_EmailOnlyPostingNeverSendsMail(t *testing.T) {
	if EmailOnlyNotice([]string{"Please send your resume to jobs@example.test to apply."}) == "" {
		t.Fatal("not read")
	}
	if EmailOnlyNotice([]string{"Questions? Contact us."}) != "" {
		t.Fatal("read from nothing")
	}
}

func TestApply_RoleGuard_SiteNameLandmarkAndSigninReadAsNoRole(t *testing.T) {
	if r := PageRole("Example Corp Careers", []string{"Example Corp Careers"}, "Example Corp Careers"); r != "" {
		t.Errorf("site name read as role %q", r)
	}
	if r := PageRole("Apply", []string{"Job Application Form"}, ""); r != "" {
		t.Errorf("landmark heading read as role %q", r)
	}
	if r := PageRole("Sign in to continue", []string{"Sign in"}, ""); r != "" {
		t.Errorf("sign-in read as role %q", r)
	}
	if r := PageRole("Staff Engineer - Example Corp", []string{"Staff Engineer"}, ""); r != "Staff Engineer" {
		t.Errorf("role %q", r)
	}
}

func TestApply_RoleGuard_StaleLinkOnlyOnADifferentJob(t *testing.T) {
	if !StaleLink("Marketing Manager", "Platform Engineer", "Example Corp") {
		t.Error("a different job is not stale")
	}
	if StaleLink("Platform Engineer (Remote)", "Platform Engineer", "Example Corp") {
		t.Error("a suffix made it stale")
	}
	// A role filed from a page title carries the employer as a tail.
	if StaleLink("Software Engineer, Jade - AI Financial Partner", "Software Engineer, Jade - AI Financial Partner at Chime", "Chime") ||
		StaleLink("Full Stack Engineer", "Full Stack Engineer  @ expensify", "Expensify") {
		t.Fatal("a company tail made the same role stale")
	}
	if !StaleLink("Platform Engineer", "Platform Engineer at Scale", "Example Corp") {
		t.Fatal("a tail naming no company was dropped")
	}
	// Scope Labs (Ashby, live): the feed wrote the company short, "@ Scope", as
	// the leading words of its name; a tail that only shares a later word is
	// another name.
	if StaleLink("Product Designer - NY", "Product Designer - NY @ Scope", "Scope Labs") {
		t.Fatal("a tail with the company's leading word made the same role stale")
	}
	if !StaleLink("Product Designer - NY", "Product Designer - NY @ Labs", "Scope Labs") {
		t.Fatal("a tail with a later word of the company was dropped")
	}
	// Rainmaker (Ashby, live): the feed wrote the company long, "@ Rainmaker
	// Technology Corporation", for the company Rainmaker.
	if StaleLink("Software Engineer, Infrastructure", "Software Engineer, Infrastructure @ Rainmaker Technology Corporation", "Rainmaker") {
		t.Fatal("a tail that opens with the company's name made the same role stale")
	}
	// A new-grad list files its rows under a cohort label ("New Grad 2027: ..."), which the
	// employer's page does not carry (Equifax); an internship label still names another job.
	if StaleLink("Data Analytics Rotational Development Associate", "New Grad 2027: Data Analytics Rotational Development Associate", "Equifax") {
		t.Fatal("a list's cohort label made the same role stale")
	}
	if !StaleLink("Software Engineer", "Intern 2026: Software Engineer", "Example Corp") {
		t.Fatal("an internship label was dropped")
	}
	if StaleLink("", "Platform Engineer", "Example Corp") || StaleLink("Anything", "", "Example Corp") {
		t.Error("no role is not stale")
	}
}

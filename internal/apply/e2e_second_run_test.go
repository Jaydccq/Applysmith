//go:build browser

package apply

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/demo"
	"applysmith/internal/gates"
)

// Synthetic copy of the controls the second live dry run with the demo
// identity left open on a Lever board: a school menu of hundreds of names that
// ends with an "Other (School Not Listed)" choice, a years-of-experience menu,
// a bare Website box, and LinkedIn and GitHub boxes whose sites accept only their own
// addresses while the persona's links are on an example domain.

const personaApplicantJSON = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 919 555 0142",
"address":{"line1":"1 Example Street","city":"Durham","state":"NC","postalCode":"27701","country":"United States"}},
"links":{"linkedin":"https://linkedin.example.org/in/alex-example","github":"https://github.example.org/alex-example","portfolio":"https://alex.example.org"},
"history":{"employment":[],"education":[]}}`

func secondRunPage(b *board) string {
	var schools strings.Builder
	for i := 0; i < 220; i++ {
		fmt.Fprintf(&schools, `<option value="Sample College %04d">Sample College %04d</option>`, i, i)
	}
	return `<title>Platform Engineer - Example Corp</title><h2>Platform Engineer</h2>
<form id="application-form" onsubmit="return false"><h4>Submit your application</h4><ul>
<li class="application-question"><label><div class="application-label">Full name<span class="required">&#x2731;</span></div><div class="application-field"><input type="text" name="name" required></div></label></li>
<li class="application-question"><label><div class="application-label">Email<span class="required">&#x2731;</span></div><div class="application-field"><input type="email" name="email" required></div></label></li>
<li class="application-question"><label><div class="application-label">LinkedIn URL<span class="required">&#x2731;</span></div><div class="application-field"><input type="text" name="urls[LinkedIn]" required></div></label></li>
<li class="application-question"><label><div class="application-label">GitHub URL</div><div class="application-field"><input type="text" name="urls[GitHub]"></div></label></li>
<li class="application-question"><div class="field-wrapper"><label for="website" class="label">Website</label><input id="website" name="website" type="text" aria-label="Website"></div></li>
<li class="application-question custom-question"><div><div class="application-label full-width dropdown"><div class="text">Which university are you currently attending or did you last attend? Please select "Other (School Not Listed)" if your school is not listed.<span class="required">&#x2731;</span></div></div>
<div class="application-field full-width required-field"><div class="application-dropdown"><select id="school" name="cards[school][field0]" required><option value="">Click Here (If you encounter an issue, make sure your browser is updated)</option>` + schools.String() + `<option value="Did not attend university">Did not attend university</option><option value="Other - School Not Listed">Other - School Not Listed</option></select></div></div></div></li>
<li class="application-question custom-question"><div><div class="application-label full-width dropdown"><div class="text">How many years of relevant, post college work experience do you have?<span class="required">&#x2731;</span></div></div>
<div class="application-field full-width required-field"><div class="application-dropdown"><select id="years" name="cards[years][field0]" required><option value="">Select...</option><option value="0">0</option><option value="1">1</option><option value="2">2</option><option value="3">3</option><option value="4">4</option><option value="5">5</option><option value="6">6</option><option value="7">7</option><option value="8">8</option><option value="9">9</option><option value="10+">10+</option></select></div></div></div></li>
</ul>
<div class="section last-section-apply"><button id="btn-submit" type="button" class="postings-btn template-btn-submit" data-qa="btn-submit" onclick="sendApplication()">Submit application</button></div>
</form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
for (const el of document.querySelectorAll('input,select')) el.addEventListener('change', () => tell((el.id || el.name) + '=' + el.value));
</script>` + submitScript(b, successHTML)
}

// The second live dry run left a school menu, a years menu and the link boxes
// open. The school the menu lacks takes its one "Other (School Not Listed)"
// choice, the years menu takes the persona's nine, the bare Website box stays
// blank by the user's decision, and a LinkedIn or GitHub link the site would
// refuse is never written: the required LinkedIn box stays empty and is reported to the user.
func TestE2E_LiveBoards_SecondRunSchoolYearsAndLinkBoxes(t *testing.T) {
	t.Parallel()
	s := liveBoard(t, "lever-apply", secondRunPage)
	// This fixture deliberately uses a link the site refuses. The shipped demo
	// now has a valid LinkedIn URL; the bank must use this fixture's same link.
	profile := strings.Replace(string(demo.File(demo.FileProfile)), "  linkedin: https://www.linkedin.com/in/alex-example-applysmith-demo", "  linkedin: https://linkedin.example.org/in/alex-example", 1)
	if !strings.Contains(profile, "\n  linkedin: https://linkedin.example.org/in/alex-example\n") {
		t.Fatal("fixture LinkedIn profile was not replaced")
	}
	useProfile(t, s.g, []byte(profile))
	s.g.applicant = personaApplicantJSON
	no := false
	s.g.facts = &match.Facts{RequiresSponsorshipNow: &no, RequiresSponsorshipFuture: &no, AuthorizedCountries: []string{"US"}}
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if s.b.presses() != 0 || run.Pressed {
		t.Fatalf("a dry run pressed: %d writes", s.b.presses())
	}
	events := strings.Join(s.b.eventList(), "\n")
	for _, want := range []string{"school=Other - School Not Listed", "years=9"} {
		if !strings.Contains(events, want) {
			t.Errorf("events lack %q:\n%s", want, events)
		}
	}
	// The bare Website box stays blank by the user's decision.
	for _, never := range []string{"linkedin.example.org", "github.example.org", "website="} {
		if strings.Contains(events, never) {
			t.Errorf("a link the site would refuse was written: %q in\n%s", never, events)
		}
	}
	var unresolved []string
	for _, p := range run.Pages {
		unresolved = append(unresolved, p.Unresolved...)
	}
	got := strings.Join(unresolved, "\n")
	if !strings.Contains(got, "LinkedIn URL") || strings.Contains(got, "GitHub") || strings.Contains(got, "Website") || strings.Contains(got, "university") || strings.Contains(got, "years") {
		t.Errorf("unresolved %v, want the required LinkedIn box and nothing else", unresolved)
	}
	if run.Outcome.Kind != OutcomeStopped || run.Outcome.Class != ClassAnswerGap {
		t.Errorf("outcome %+v, want a stop that leaves the LinkedIn box to the user", run.Outcome)
	}
	// What the engine reported for the two refused links: failed, with its own reason.
	log, err := os.ReadFile(strings.TrimSuffix(run.Snapshot, ".html") + ".fill-log.jsonl")
	if err != nil {
		t.Fatalf("fill log: %v", err)
	}
	for _, label := range []string{"LinkedIn URL", "GitHub URL"} {
		want := `"label":"` + label + `","kind":"text","outcome":"failed","reason":"format-not-accepted"`
		if !strings.Contains(string(log), want) {
			t.Errorf("the fill log lacks %s\n%s", want, log)
		}
	}
}

package match

import "testing"

func TestModelWritten(t *testing.T) {
	for _, c := range []struct {
		text         string
		choice, long bool
		want         bool
	}{
		{"Why do you want to work at Example Corp?", false, false, true},
		{"Why TypeSafe?", false, false, true},
		{"What excited you about this role? You can check out our video about the team", false, false, true},
		{"What career accomplishments are you proud to share?", false, false, true},
		{"Have you previously supported a SaaS company or a custom software application?", false, false, true},
		{"Do you have any hands-on experience with Kubernetes?", false, false, true},
		{"Notes for the hiring team", false, true, true},
		{"What was a model that you have built for someone else, how was it useful for them and what were its limitations? Keep it short and sweet.", false, false, true},
		{"What was the most complex piece of logic that you have implemented within a commercial application ? Keep it short and sweet.", false, false, true},
		{"What have you built that you are proudest of?", false, false, true},
		{"What is the best idea you have ever had?", false, false, true},
		{"Tell us about an idea you've come up with outside of work", false, false, true},
		{"What is the name of your best idea's product?", false, false, false},
		{"What did you build at your last employer?", false, false, false},
		{"What is the name of the product you shipped?", false, false, false},
		{"What other languages do you use?", false, false, false},
		{"How many years of Go experience do you have?", false, false, false},
		{"How did you hear about us?", false, false, false},
		{"Why did you choose this team?", true, false, false},
		{"Have you previously worked for Example Corp?", false, false, false},
		{"Have you used AI to complete this application?", false, false, false},
		{"Are you legally authorized to work in this country? Why not?", false, false, false},
		{"Why is your expected salary what it is?", false, false, false},
		{"Why do you want to start in May?", false, false, false},
		{"Why do you want to start in May? Tell us more about your plans.", false, false, false},
		{"New Grad 2027 Application Questions : Why are you interested in Nominal? We want to know why you want to work here and what skills you hope to develop in your first full-time role out of school, a great answer is around 100 words max. Feel free to reference specific things about our product, mission, or team.", false, false, true},
		{"What is the name of your last employer and why did you leave?", false, false, false},
		{"Please provide your mailing address", false, true, false},
		{"Do you think it's ethical to automate people's jobs?", true, false, true},
		{"Do you believe AI will change how software is built?", true, false, true},
		{"Do you think you meet the minimum qualifications?", true, false, false},
		{"Do you believe you will require sponsorship?", true, false, false},
		{"Do you agree to the privacy policy?", true, false, false},
	} {
		if got := ModelWritten(c.text, c.choice, c.long); got != c.want {
			t.Errorf("%q (choice %v, long %v): %v, want %v", c.text, c.choice, c.long, got, c.want)
		}
	}
}

package match

import "testing"

// dComp builds a curated compensation entry with the question as its example.
func dComp(question string, mods ...func(*Entry)) Entry {
	e := Entry{ID: "salary-expectation", Kind: KindCompensationRange,
		Body: Body{Minimum: "90000", Maximum: "130000", DefaultAnswer: "120000", Currency: "USD"},
		Meta: Meta{Examples: []string{question}}}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func compAnswer(t *testing.T, e Entry, q Question) Result {
	t.Helper()
	return pfOne(t, pfMatcher(e), Facts{}, Date{}, q)
}

func TestAnswerBank_Compensation_MonthlyIsAnnualOver12RoundedTrimmed(t *testing.T) {
	tests := []struct {
		annual, want string
		ok           bool
	}{
		{"120000", "10000", true},
		{"126000", "10500", true},
		{"125000", "10416.67", true},
		{"1,200,000", "100000", true},
		{"1.5", "0.13", true}, // exact tie 0.125 rounds up, as toFixed does
		{"0.3", "0.03", true},
		{"120000.00", "10000", true},
		{"about 100k", "", false},
		{"0", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := compForPeriod(tc.annual, "Desired monthly salary")
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q = %q,%v want %q,%v", tc.annual, got, ok, tc.want, tc.ok)
		}
	}
	if got, _ := compForPeriod("120000", "Desired annual salary"); got != "120000" {
		t.Errorf("annual question: %q", got)
	}
	const q = "Desired monthly salary"
	if r := compAnswer(t, dComp(q), textQ("s", q)); r.Answer != "10000" {
		t.Fatalf("%+v", r)
	}
	bad := dComp(q, func(e *Entry) { e.DefaultAnswer = "negotiable" })
	if r := compAnswer(t, bad, textQ("s", q)); r.Outcome != OutcomeAbstained {
		t.Fatalf("a figure that is not a number has no monthly form: %+v", r)
	}
	curQ := "Monthly salary currency"
	if r := compAnswer(t, dComp(curQ), textQ("s", curQ)); r.Answer != "USD" {
		t.Fatalf("the currency is never period-converted: %+v", r)
	}
}

func TestAnswerBank_Compensation_PayPeriodPickerAnswersAnnualOption(t *testing.T) {
	const q = "Which period does your salary expectation refer to?"
	sel := func(labels ...string) Question { return pfSelect("s", q, labels...) }
	r := compAnswer(t, dComp(q), sel("Hourly", "Weekly", "Monthly", "Yearly"))
	if r.ChosenLabel != "Yearly" || r.Outcome == OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	if r := compAnswer(t, dComp(q), sel("Select...", "Per hour", "Per annum")); r.ChosenLabel != "Per annum" {
		t.Fatalf("%+v", r)
	}
	blank := dComp(q, func(e *Entry) { e.DefaultAnswer = "" })
	if r := compAnswer(t, blank, sel("Hourly", "Monthly", "Yearly")); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("no configured amount: %+v", r)
	}
	// No annual option: not a period picker, so the amount is bound instead.
	if r := compAnswer(t, dComp(q), sel("Hourly", "Weekly", "Monthly")); r.ChosenLabel == "Hourly" {
		t.Fatalf("%+v", r)
	}
	// An amount picker still answers with the amount.
	amt := compAnswer(t, dComp(q), sel("$100,000 - $125,000", "$125,000 - $150,000"))
	if amt.ChosenLabel != "$100,000 - $125,000" {
		t.Fatalf("%+v", amt)
	}
}

func TestAnswerBank_Compensation_FloorMenuAnswersFromMinimum(t *testing.T) {
	const q = "What is your expected salary?"
	menu := func(labels ...string) Question { return pfSelect("s", q, labels...) }
	floors := []string{"-- Please Specify --", "$50,000+", "$75,000+", "$100,000+", "$125,000+"}
	r := compAnswer(t, dComp(q), menu(floors...))
	if r.ChosenLabel != "$75,000+" || r.Outcome != OutcomeFilledReview {
		t.Fatalf("highest floor not above the 90000 minimum, in the review band: %+v", r)
	}
	low := dComp(q, func(e *Entry) { e.Minimum = "20000" })
	if r := compAnswer(t, low, menu(floors...)); r.Outcome != OutcomeAbstained {
		t.Fatalf("a minimum below every floor abstains: %+v", r)
	}
	maxQ := "What is your maximum expected salary?"
	if r := compAnswer(t, dComp(maxQ), pfSelect("s", maxQ, floors...)); r.ChosenLabel != "$125,000+" {
		t.Fatalf("a named maximum is answered from the maximum: %+v", r)
	}
	noMin := dComp(q, func(e *Entry) { e.Minimum = "" })
	if r := compAnswer(t, noMin, menu(floors...)); r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	for _, words := range [][]string{
		{"$75,000 or more", "$100,000 or above", "$125,000 and up"},
		{"$75,000 plus", "$100,000 plus"},
	} {
		if r := compAnswer(t, dComp(q), menu(words...)); r.Outcome == OutcomeAbstained {
			t.Fatalf("%v: %+v", words, r)
		}
	}
	// One closed band makes it a bracket menu, not a floor menu.
	mixed := menu("$50,000+", "$75,000+", "$100,000 - $125,000")
	if r := compAnswer(t, dComp(q), mixed); r.ChosenLabel == "$75,000+" {
		t.Fatalf("closed band disqualifies the floor menu: %+v", r)
	}
	if _, ok := compFloorMenu([]Option{{Label: "C++ experience, $80,000"}, {Label: "$90,000+"}}); ok {
		t.Fatal("a stray plus inside prose is not a floor")
	}
}

func TestAnswerBank_Compensation_NoContainingBracketAbstains(t *testing.T) {
	const q = "Please select your expected salary band"
	bands := pfSelect("s", q, "$50,000 - $75,000", "$75,000 - $100,000")
	r := compAnswer(t, dComp(q), bands)
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("a target above every band must not fill the nearest one: %+v", r)
	}
	in := compAnswer(t, dComp(q, func(e *Entry) { e.DefaultAnswer = "80000" }), bands)
	if in.ChosenLabel != "$75,000 - $100,000" {
		t.Fatalf("%+v", in)
	}
	// Contiguous brackets: the figure on the shared edge is in the bracket where it is the lower bound.
	edge := pfSelect("s", q, "110,000 to 120,000", "120,000 to 130,000")
	if r := compAnswer(t, dComp(q), edge); r.ChosenLabel != "120,000 to 130,000" {
		t.Fatalf("%+v", r)
	}
	// Stanford writes each edge as annual/hourly: a pair is one amount, read by its annual figure.
	const sq = "7. Desired Salary Range (Annual/Hourly)"
	paired := pfSelect("s", sq, "100,000/48.07 - 110,000/52.88", "110,000/52.88 - 120,000/57.69", "120,000/57.69 - 130,000/62.50", "130,000/62.50 - 140,000/67.30", "350,000/168.26")
	if r := compAnswer(t, dComp(sq), paired); r.ChosenLabel != "120,000/57.69 - 130,000/62.50" {
		t.Fatalf("%+v", r)
	}
	// A ceiling answer bounds its own bucket.
	const tq = "How much will you travel?"
	travel := pfSelect("s", tq, "0%", "0-25%", "25-50%")
	e := dComp(tq, func(e *Entry) { e.DefaultAnswer = "Up to 25%" })
	if r := compAnswer(t, e, travel); r.ChosenLabel != "0-25%" {
		t.Fatalf("%+v", r)
	}
}

// Mindbody (Greenhouse, live): "in your local currency" is the amount's unit,
// not a request for the currency, and a bracket menu priced in several
// currencies is read only in the entry's own: BRL 100,000 - 150,000 holds the
// figure too, but it is not the entry's money.
func TestAnswerBank_Compensation_BracketsInSeveralCurrencies(t *testing.T) {
	const q = "What are your desired base salary expectations in your local currency?"
	menu := pfSelect("s", q, "USD $110,000 - $120,000", "USD $120,000 - $130,000", "MXN $2,000,000 - $2,200,000", "BRL $100,000 - $150,000")
	req := pfReq(Facts{}, Date{}, menu)
	req.Posting.Location = "United States"
	if r := pfRun(t, pfMatcher(dComp(q)), req)[0]; r.ChosenLabel != "USD $120,000 - $130,000" {
		t.Fatalf("%+v", r)
	}
	// An entry in a currency the menu does not price finds no bracket.
	brl := pfReq(Facts{}, Date{}, pfSelect("s", "Desired salary", "USD $110,000 - $120,000", "MXN $2,000,000 - $2,200,000"))
	if r := pfRun(t, pfMatcher(dComp("Desired salary", func(e *Entry) { e.Currency = "BRL" })), brl)[0]; r.Outcome != OutcomeAbstained {
		t.Fatalf("a BRL figure filled a bracket in another currency: %+v", r)
	}
}

func TestAnswerBank_Compensation_CurrencyDenominationGuard(t *testing.T) {
	loc := func(l string) func(*Request) { return func(r *Request) { r.Posting.Location = l } }
	// Camunda (Ashby, live): "your local currency" on a "Remote" posting is the
	// recorded address's; a posting a country places keeps its own.
	home := func(l, country string) func(*Request) {
		return func(r *Request) {
			r.Posting.Location, r.Facts.Address = l, AddressFact{City: "Example", Country: country}
		}
	}
	tests := []struct {
		name, text, currency string
		req                  func(*Request)
		blocked              bool
	}{
		{"named currency matches", "Desired salary in USD", "USD", nil, false},
		{"named currency differs", "Desired salary in EUR", "USD", nil, true},
		{"one of several named matches", "Salary in USD or EUR", "USD", nil, false},
		{"local currency, US posting, USD entry", "Desired salary in local currency", "USD", loc("Exampleville, TX, United States"), false},
		{"local currency, UK posting, USD entry", "Desired salary in local currency", "USD", loc("Exampleton, United Kingdom"), true},
		{"local currency, GBP entry, UK posting", "Desired salary in local currency", "GBP", loc("Exampleton, United Kingdom"), false},
		{"local currency, unknown posting", "Desired salary in local currency", "USD", nil, true},
		{"local currency, remote posting, US home", "What are your salary expectations? Please include your local currency , and a range of minimum to maximum", "USD", home("Remote", "US"), false},
		{"local currency, remote posting, Canadian home", "Desired salary in local currency", "USD", home("Remote", "CA"), true},
		{"local currency, UK posting, US home", "Desired salary in local currency", "USD", home("Exampleton, United Kingdom", "US"), true},
		{"local currency with explicit code that matches", "Desired salary in local currency (EUR)", "EUR", nil, false},
		{"a code beyond the first ten", "Desired salary in SEK", "USD", nil, true},
		{"a euro sign", "Desired salary (\u20ac)", "USD", nil, true},
		{"a euro sign that matches", "Desired salary (\u20ac)", "EUR", nil, false},
		{"a currency named in words", "What is your annual gross salary expectation in Euros?", "USD", nil, true},
		{"a currency named in words that matches", "Desired salary in US dollars", "USD", nil, false},
		{"a bare dollar names no one currency", "Desired salary in dollars", "USD", nil, false},
		{"local currency, Swedish posting, USD entry", "Desired salary in local currency", "USD", loc("Exampleby, Sweden"), true},
		{"no currency named", "Desired salary", "USD", nil, false},
		{"a lower-case word is not a code", "Desired salary, chf aside", "USD", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := dComp(tc.text, func(e *Entry) { e.Currency = tc.currency })
			req := pfReq(Facts{}, Date{}, textQ("s", tc.text))
			if tc.req != nil {
				tc.req(&req)
			}
			r := pfRun(t, pfMatcher(e), req)[0]
			got := r.Outcome == OutcomeAbstained && r.Reason == ReasonContextPolicy && r.Detail == DetailLocalCurrency && r.Terminal
			if got != tc.blocked {
				t.Fatalf("blocked=%v, result %+v", got, r)
			}
		})
	}
	t.Run("a captured-scope entry is not guarded", func(t *testing.T) {
		if _, _, hit := currencyGuard(&Step{Q: Question{Text: "Salary in EUR"}, Req: &Request{}}, &Entry{Kind: KindCompensationRange, Meta: Meta{CapturedScope: []ScopeItem{{Host: "h"}}}}); hit {
			t.Fatal("blocked")
		}
	})
}

// A salary range asked in two text boxes is answered from the range the user
// stored: the beginning from the minimum and the end from the maximum, never
// one end from the other, written as the question asks. Example figures in
// several currencies show the format and name none; a code, a sign outside the
// figures, "local currency" or a posting known to pay in another currency
// still holds the entry back.
func TestAnswerBank_Compensation_RangeEndsAnswerFromTheStoredRange(t *testing.T) {
	const (
		// The two boxes of an Ashby form, as a user's run met them.
		begin  = "What is the beginning of your desired annual base salary range? (please format with no commas, i.e. $50000, \u00a350000)"
		end    = "What is the end of your desired annual base salary range? (please format with no commas, i.e. $70000, \u00a370000)"
		dollar = "What is the beginning of your desired salary range? (please format with no commas, e.g. $50000)"
		us     = "Exampleville, TX, United States"
		uk     = "Exampleton, United Kingdom"
	)
	tests := []struct {
		name, text, location string
		entry                func(*Entry)
		want                 string
		reason               Reason // set when the question abstains
		detail               Detail
	}{
		{"the beginning is the minimum", begin, us, nil, "90000", "", ""},
		{"the end is the maximum", end, us, nil, "130000", "", ""},
		{"the start of the range", "What is the start of your salary range?", "", nil, "90000", "", ""},
		{"the lower end is not the end", "What is the lower end of your desired salary range?", "", nil, "90000", "", ""},
		{"the upper end", "What is the upper end of your desired salary range?", "", nil, "130000", "", ""},
		{"a starting salary is the target, not a range end", "What is your desired starting salary?", "", nil, "120000", "", ""},
		{"no minimum leaves the beginning to the user", begin, us, func(e *Entry) { e.Minimum = "" }, "", ReasonPlaceholderEmpty, ""},
		{"no commas and no sign", begin, us, func(e *Entry) { e.Minimum = "$90,000" }, "90000", "", ""},
		{"a k figure is written out", end, us, func(e *Entry) { e.Maximum = "130k" }, "130000", "", ""},
		{"the one sign of the example is kept", dollar, "", nil, "$90000", "", ""},
		{"a dollar example never marks another currency", dollar, "", func(e *Entry) { e.Currency = "EUR" }, "90000", "", ""},
		{"a value that is not one figure is left to the user", begin, us, func(e *Entry) { e.Minimum = "90k to 100k" }, "", ReasonPlaceholderEmpty, ""},
		{"no format asked keeps the stored text", "What is the beginning of your desired annual base salary range?", "", func(e *Entry) { e.Minimum = "90,000" }, "90,000", "", ""},
		{"examples name no currency for a posting with no place", begin, "", nil, "90000", "", ""},
		{"examples hold the entry to a posting known abroad", begin, uk, nil, "", ReasonContextPolicy, DetailLocalCurrency},
		{"examples in one currency name it", "What is the beginning of your desired salary range? (e.g. \u00a350000, \u00a370000)", us, nil, "", ReasonContextPolicy, DetailLocalCurrency},
		{"a code beside the examples", "What is the beginning of your desired salary range in EUR? (i.e. $50000, \u00a350000)", us, nil, "", ReasonContextPolicy, DetailLocalCurrency},
		{"local currency beside the examples, place unknown", "What is the beginning of your desired salary range in local currency? (i.e. $50000, \u00a350000)", "", nil, "", ReasonContextPolicy, DetailLocalCurrency},
		{"a sign outside the examples", "What is the beginning of your desired salary range, \u00a3 only? (i.e. $50000, \u00a350000)", us, nil, "", ReasonContextPolicy, DetailLocalCurrency},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := dComp(tc.text)
			if tc.entry != nil {
				tc.entry(&e)
			}
			req := pfReq(Facts{}, Date{}, textQ("s", tc.text))
			req.Posting.Location = tc.location
			r := pfRun(t, pfMatcher(e), req)[0]
			switch {
			case tc.reason != "" && (r.Outcome != OutcomeAbstained || r.Reason != tc.reason || r.Detail != tc.detail):
				t.Fatalf("want abstain %s/%s, got %+v", tc.reason, tc.detail, r)
			case tc.reason == "" && (r.Outcome == OutcomeAbstained || r.Answer != tc.want):
				t.Fatalf("want %q, got %+v", tc.want, r)
			}
		})
	}
}

func TestAnswerBank_HeldCompensation_NeverTakesTargetFigure(t *testing.T) {
	held := []string{
		"What forms of deferred compensation do you currently have?",
		"Do you have any unvested equity?",
		"Which forms of equity do you currently hold at your employer?",
		"Do you currently hold any RSUs, restricted stock or options?",
		"List the stock options you currently have in your name.",
		"What is your current monthly net (after tax) salary?",
		"What is your present annual base pay?",
		"State your current total compensation.",
	}
	target := []string{
		"What is your current salary expectation?",
		"What is your present pay requirement?",
		"What is your current salary range?",
		"What is your desired salary?",
		"Do you have experience with equity compensation?",
	}
	for _, q := range held {
		if !asksHeldCompensation(q) {
			t.Errorf("held fact not detected: %q", q)
		}
	}
	for _, q := range target {
		if asksHeldCompensation(q) {
			t.Errorf("target question misread as held: %q", q)
		}
	}
	for _, q := range held[:2] {
		for _, e := range []Entry{
			dComp(q),
			dFixed("salary-expectation", "Target total compensation: 100K to 250K USD", q),
		} {
			r := pfOne(t, pfMatcher(e), Facts{}, Date{}, textQ("s", q))
			if r.Outcome != OutcomeAbstained || r.Reason != ReasonContextPolicy || r.Detail != DetailHeldCompensation || !r.Terminal {
				t.Fatalf("%q with %s: %+v", q, e.Kind, r)
			}
		}
	}
	// An entry that states the held fact still answers.
	q := held[0]
	if r := pfOne(t, pfMatcher(dFixed("deferred-compensation", "N/A", q)), Facts{}, Date{}, textQ("s", q)); r.Answer != "N/A" {
		t.Fatalf("%+v", r)
	}
	// The target question is still answered from the target entry.
	tq := "What is your current salary expectation?"
	if r := pfOne(t, pfMatcher(dComp(tq)), Facts{}, Date{}, textQ("s", tq)); r.Answer != "120000" {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_Compensation_NumberHelpers(t *testing.T) {
	tests := []struct {
		label    string
		min, max float64
		hasMin   bool
		hasMax   bool
		ok       bool
	}{
		{"$90,000+", 90000, 0, true, false, true},
		{"$75k or more", 75000, 0, true, false, true},
		{"Up to 25%", 0, 25, false, true, true},
		{"0-25%", 0, 25, true, true, true},
		{"110k to 120k", 110000, 120000, true, true, true},
		{"At least $5", 5, 0, true, false, true},
		// Climate First Bank (Greenhouse, live): the open brackets are signs.
		{"≥100,001", 100001, 0, true, false, true},
		{"≤40,000", 0, 40000, false, true, true},
		{"$100,000", 0, 0, false, false, false},
		{"Negotiable", 0, 0, false, false, false},
	}
	for _, tc := range tests {
		r, ok := compRangeOf(tc.label)
		if ok != tc.ok {
			t.Errorf("%q ok=%v", tc.label, ok)
			continue
		}
		if !ok {
			continue
		}
		if (r.Min != nil) != tc.hasMin || (r.Max != nil) != tc.hasMax || (tc.hasMin && *r.Min != tc.min) || (tc.hasMax && *r.Max != tc.max) {
			t.Errorf("%q = %+v", tc.label, r)
		}
	}
	if n := compNumbers("100 months"); len(n) != 1 || n[0].Value != 100 {
		t.Errorf("a suffix glued to a word is not a multiplier: %v", n)
	}
}

// A bound option answers with its value and reports its label, like every
// option resolver: the control takes the value.
func TestAnswerBank_ProfileOptionDraftAnswersWithTheValue(t *testing.T) {
	const text = "Preferred salary currency"
	q := Question{Selector: "s", Text: text, Kind: KindSelect,
		Options: []Option{{Value: "cur-eur", Label: "EUR"}, {Value: "cur-usd", Label: "USD"}, {Value: "cur-gbp", Label: "GBP"}}}
	r := compAnswer(t, dComp(text), q)
	if r.Answer != "cur-usd" || r.ChosenLabel != "USD" {
		t.Fatalf("want value cur-usd with label USD, got %+v", r)
	}
}

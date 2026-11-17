package match

import (
	"strings"
	"testing"
)

// Databricks (Greenhouse embed): a sanctions screen and its follow-up.
var (
	sanctionsScreen = Question{Selector: "s", Kind: KindCheckbox,
		Text: "Please confirm whether any of the below applies to you. Select all that apply. Note: This information will only be used to ensure compliance with U.S. sanctions and export controls.",
		Options: opts("Citizen or permanent resident of Cuba, Iran, North Korea, or Syria",
			"Ordinarily a resident of Cuba, Iran, North Korea, Syria or the Crimea, Donetsk, Luhansk, Zaporizhzhia, or Kherson regions of Ukraine",
			"Ordinarily a resident of Russia or Belarus and not willing to relocate for a Databricks role", "None of the above")}
	sanctionsFollow = Question{Selector: "f", Kind: KindCheckbox,
		Text: "If you selected a response to the prior question other than “none of the above,” please confirm whether any of the following also applies to you. Select all that apply.",
		Options: opts("U.S. citizen", "U.S. non-citizen national", "U.S. permanent resident (Green Card holder)", "Individual granted asylum in the U.S.",
			"Individual granted refugee status in the U.S.", "Individual granted citizenship in a country other than Cuba, Iran, North Korea, or Syria",
			"Individual granted permanent residency in a country other than Cuba, Iran, North Korea, or Syria", "None of these apply to me",
			"Not applicable (i.e., I selected “none of the above” for the prior question)")}
)

func living(citizenships []string, country string) func(*Request) {
	return func(r *Request) {
		r.Facts.Citizenships = citizenships
		r.Facts.Address.Country = country
	}
}

func TestAnswerBank_Sanctions_ScreenFromNationalityAndAddress(t *testing.T) {
	m := pickMatcher()
	r := matchReq(m, sanctionsScreen, living([]string{"CN"}, "US"))
	if strings.Join(r.ChosenLabels, "|") != "None of the above" {
		t.Fatalf("a Chinese citizen living in the US: %+v", r)
	}
	f := matchReq(m, sanctionsFollow, living([]string{"CN"}, "US"))
	if !strings.HasPrefix(strings.Join(f.ChosenLabels, "|"), "Not applicable") {
		t.Fatalf("the follow-up of none of the above: %+v", f)
	}
	// Every citizenship of a dual national is read.
	if r := matchReq(m, sanctionsScreen, living([]string{"CN", "IR"}, "US")); r.Outcome != OutcomeAbstained || r.Reason != ReasonContextPolicy {
		t.Fatalf("an Iranian dual national: %+v", r)
	}
	if f := matchReq(m, sanctionsFollow, living([]string{"CN", "IR"}, "US")); f.ChosenLabel != "" {
		t.Fatalf("the follow-up of a statement that holds: %+v", f)
	}
	// Residence: a Russian address holds the third statement; a Ukrainian one
	// cannot be told from the sanctioned regions.
	for _, home := range []string{"RU", "UA"} {
		if r := matchReq(m, sanctionsScreen, living([]string{"CN"}, home)); r.Outcome != OutcomeAbstained {
			t.Fatalf("an address in %s: %+v", home, r)
		}
	}
	// Nothing recorded is left for the applicant.
	if r := matchReq(m, sanctionsScreen, living(nil, "US")); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("no recorded citizenship: %+v", r)
	}
	if r := matchReq(m, sanctionsScreen, living([]string{"CN"}, "")); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("no recorded address country: %+v", r)
	}
}

func TestAnswerBank_Sanctions_ListedCountryReadsEveryCitizenship(t *testing.T) {
	// An export-control Yes/No listing countries is Yes for a dual national
	// whose second citizenship is listed, with no country-of-citizenship entry.
	q := radioQ("Are you a citizen of any of the following countries: Cuba, Iran, North Korea, Syria?", "Yes", "No")
	r := matchReq(pickMatcher(), q, living([]string{"CN", "IR"}, "US"))
	if r.ChosenLabel != "Yes" || !traceHas(r, "a recorded citizenship") {
		t.Fatalf("an Iranian dual national: %+v", r)
	}
}

// Asana (Greenhouse): an export-control Yes/No that names the sanctioned
// places in its own question is answered from nationality and address.
func TestAnswerBank_Sanctions_YesNoThatNamesThePlaces(t *testing.T) {
	const preamble = "Asana is seeking the below information for the exclusive and limited purpose of complying with U.S. export control laws given that the position for which you are applying may require access to technology subject to those laws. We will not use the information that you provide for any other purpose. "
	q := radioQ(preamble+"Are you a citizen or legal permanent resident of Cuba, Iran, North Korea, Syria or Ukraine (Crimea region)?", "Yes", "No")
	m := pickMatcher()
	if r := matchReq(m, q, living([]string{"CN"}, "US")); r.ChosenLabel != "No" || !traceHas(r, "sanctions Yes/No") {
		t.Fatalf("a Chinese citizen living in the US: %+v", r)
	}
	for name, l := range map[string]func(*Request){
		"an Iranian dual national": living([]string{"CN", "IR"}, "US"),
		"an address in Ukraine":    living([]string{"CN"}, "UA"),
		"no recorded citizenship":  living(nil, "US"),
		"no recorded address":      living([]string{"CN"}, ""),
	} {
		if r := matchReq(m, q, l); r.ChosenLabel == "No" {
			t.Fatalf("%s is answered No: %+v", name, r)
		}
	}
	// Citizenship alone does not read the address.
	only := radioQ("Are you a citizen of Cuba, Iran, North Korea or Syria?", "Yes", "No")
	if r := matchReq(m, only, living([]string{"CN"}, "")); r.ChosenLabel != "No" {
		t.Fatalf("a citizenship question with no address: %+v", r)
	}
	// A question that asks to confirm the applicant is no citizen of them
	// reverses what a No says.
	for _, text := range []string{
		"Can you confirm that you are not a citizen or resident of Cuba, Iran, North Korea, or Syria?",
		"Do you certify you are neither a citizen nor a resident of Cuba, Iran, North Korea or Syria?",
	} {
		if r := matchReq(m, radioQ(text, "Yes", "No"), living([]string{"CN"}, "US")); r.ChosenLabel == "No" {
			t.Fatalf("%q is answered No: %+v", text, r)
		}
	}
	// A list that reaches past what it names, or names another country, is
	// not answered No.
	for _, text := range []string{
		"Are you a citizen of Cuba, Iran, North Korea, Syria or any other country subject to U.S. sanctions?",
		"Are you a citizen of Cuba, Iran or Canada?",
		"Are you a citizen of Cuba?",
	} {
		if r := matchReq(m, radioQ(text, "Yes", "No"), living([]string{"CN"}, "US")); traceHas(r, "sanctions Yes/No") {
			t.Fatalf("%q: %+v", text, r)
		}
	}
}

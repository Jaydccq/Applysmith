package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

func TestXC_RoutesConsent(t *testing.T) {
	src := map[string]xcBSet{
		"job-source": xcBPref("LinkedIn"), "referral-source": xcBPref("Employee referral"), "marketing-text-consent": xcBAns("Yes"),
		"future-communications-consent": xcBPref("Yes"), "interview-recording-consent": xcBPref("Yes"), "privacy-policy-consent": xcBAns("Yes"),
	}
	marketing := map[string]xcBSet{"marketing-email-optin": xcBAns("Yes"), "marketing-text-consent": xcBAns("Yes")}
	one := func(text, label string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindCheckbox, Required: true, Options: xcBOpts(label)}
	}
	menu := func(text, label string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindSelect, Required: true, Options: xcBOpts("Select an option", label)}
	}
	xcBRunCases(t, []xcBCase{
		// XC-057
		{name: "XC-057 how did you hear", sets: src, facts: xcBFacts(),
			q: xcBQ("How did you hear about us?", match.KindSelect, "Referral", "LinkedIn", "Other"), entry: "job-source", label: "LinkedIn"},
		{name: "XC-057 become aware of this position", sets: src, facts: xcBFacts(),
			q: xcBQ("How did you become aware of this position?", match.KindSelect, "Referral", "LinkedIn", "Other"), entry: "job-source", label: "LinkedIn"},
		{name: "XC-057 first see this posting", sets: src, facts: xcBEmployment(),
			q: xcBQ("Where did you first see this posting?", match.KindSelect, "Referral", "LinkedIn", "Other"), entry: "job-source", label: "LinkedIn"},
		// Qualtrics (Greenhouse, live): "have you seen", asked as a checkbox group.
		{name: "XC-057 where have you seen the company", sets: src, facts: xcBFacts(),
			q: xcBQ("Where have you seen Qualtrics prior to applying? Select all that apply.", match.KindCheckbox,
				"I am a member of the Qualtrics Talent Community", "I attended a networking event or career fair",
				"I have seen Qualtrics on social media (LinkedIn, Facebook, Instagram, TikTok, etc)", "None of the above"),
			entry: "job-source", answer: "I have seen Qualtrics on social media (LinkedIn, Facebook, Instagram, TikTok, etc)"},
		// Avride (Greenhouse, live): the "other" choice's "(please describe fully)" in the example list asks for no essay.
		{name: "XC-057 how did you hear in a text box, with an example list", sets: src, facts: xcBFacts(),
			q: match.Question{Selector: "s", Kind: match.KindText, InputType: match.InputLongText, Required: true,
				Text: "How did you hear about this position? (Please provide specific details.) For example: Indeed, LinkedIn, ZipRecruiter, Facebook group (name of group), employee referral (name of employee), recruiter outreach or other (please describe fully)."},
			entry: "referral-source", answer: "Employee referral"},
		// Resend (Ashby): a closing "Whether it was through a friend, ..." lists channels and asks for no story.
		{name: "XC-057 how did you hear in a text box, with a closing list of channels", sets: src, facts: xcBFacts(),
			q: match.Question{Selector: "s", Kind: match.KindText, InputType: match.InputLongText, Required: true,
				Text: "How did you hear about us? We’d love to know how you found us! Whether it was through a friend, social media, a job board, or somewhere else, feel free to share."},
			entry: "referral-source", answer: "Employee referral"},
		// Windfall (Lever, queue785): examples in parentheses, then a referrer's name if referred; a story asked after them is not the source.
		{name: "XC-057 how did you hear in a text box, with examples in parentheses", sets: src, facts: xcBFacts(),
			q: match.Question{Selector: "s", Kind: match.KindText, InputType: match.InputLongText, Required: true,
				Text: "How did you hear about Windfall? (e.g. LinkedIn, social network, referral, etc) If a referral, please state the referral's name."},
			entry: "referral-source", answer: "Employee referral"},
		{name: "XC-057 examples in parentheses, then a story", sets: src, facts: xcBFacts(),
			q: match.Question{Selector: "s", Kind: match.KindText, InputType: match.InputLongText, Required: true,
				Text: "How did you hear about Windfall? (e.g. LinkedIn, referral) Tell us what drew you to the role."},
			notEntry: "referral-source", wantNone: true},
		{name: "XC-057 text message authorization", sets: src, facts: xcBFacts(),
			q: xcBYesNo("I authorize Example Corp to communicate with me via text message"), entry: "marketing-text-consent", label: "Yes"},
		{name: "XC-057 future positions communications", sets: src, facts: xcBFacts(),
			q: xcBQ("May we contact you regarding future positions? Do you wish to receive communications?", match.KindRadioGroup,
				"Yes, I would like to receive communications", "No, please do not contact me"),
			entry: "future-communications-consent", label: "Yes, I would like to receive communications"},
		{name: "XC-057 interview recording picker", sets: src, facts: xcBFacts(),
			q: xcBYesNo("Do you consent to being recorded and transcribed during your interview?"), entry: "interview-recording-consent", label: "Yes"},
		// HelloFresh (Greenhouse, live): the notice heading names the recording; the consent is in the options.
		{name: "XC-057 interview recording notice states its consent in the options", sets: src, facts: xcBFacts(),
			q: xcBQ("Interview Recording and AI Notetaker — Notice", match.KindSelect,
				"I consent to the recording and AI transcription of my interview.", "I do not consent. (We will not use the notetaker for your interview.)"),
			entry: "interview-recording-consent", label: "I consent to the recording and AI transcription of my interview."},
		{name: "XC-057 plain privacy checkbox is not the recording picker", sets: src, facts: xcBFacts(), legal: true,
			q: one("I consent to the processing of my personal data", "I consent"), notEntry: "interview-recording-consent", wantNone: true},
		{name: "XC-057 sms consent from option shape", sets: src, facts: xcBFacts(),
			q:     xcBQ("Communication preferences", match.KindRadioGroup, "Yes, I opt in to receive SMS messages", "No, do not send text messages"),
			entry: "marketing-text-consent", label: "Yes, I opt in to receive SMS messages"},

		// XC-058
		{name: "XC-058 job alerts and newsletter", sets: marketing, facts: xcBFacts(),
			q: xcBYesNo("Would you like to receive job alerts and stay up to date with our newsletter?"), entry: "marketing-email-optin", label: "Yes"},
		{name: "XC-058 text updates agreement", sets: marketing, facts: xcBFacts(),
			q: xcBYesNo("Check Yes or No to indicate your agreement to receive text message updates"), entry: "marketing-text-consent", label: "Yes"},
		{name: "XC-058 authorize via text", sets: marketing, facts: xcBFacts(),
			q: xcBYesNo("I authorize Example Corp to contact me via text"), entry: "marketing-text-consent", label: "Yes"},
		// Visa (Workday, live): required Opt-In / Opt-Out lists; the recorded consent decides.
		{name: "XC-058 SMS about the application, Opt-Out", sets: map[string]xcBSet{"marketing-text-consent": xcBAns("No")}, facts: xcBFacts(),
			q:     xcBQ("I agree that Visa may reach out to me via SMS regarding my application and candidate experience. Message and data rates may apply. I can opt-out at any time.", match.KindSelect, "Opt-In", "Opt-Out"),
			entry: "marketing-text-consent", label: "Opt-Out"},
		{name: "XC-058 SMS about the application, Opt-In", sets: marketing, facts: xcBFacts(),
			q:     xcBQ("I agree that Visa may reach out to me via SMS regarding my application and candidate experience.", match.KindSelect, "Opt-In", "Opt-Out"),
			entry: "marketing-text-consent", label: "Opt-In"},
		{name: "XC-058 automated tools review the application", sets: map[string]xcBSet{"aedt-consent": xcBPref("No")}, facts: xcBFacts(),
			q:     xcBQ("Visa may use automated tools such as AI to support review of your application for this role, and if applicable, match you with relevant existing and future open positions.", match.KindSelect, "Opt-In", "Opt-Out"),
			entry: "aedt-consent", label: "Opt-Out"},
		{name: "XC-058 the applicant's own AI use is no automated tool", sets: map[string]xcBSet{"aedt-consent": xcBPref("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("Did you use AI to review your application?"), notEntry: "aedt-consent", wantNone: true},
		{name: "XC-058 generic optional opt-in stays unticked", sets: map[string]xcBSet{"privacy-policy-consent": xcBAns("Yes")}, facts: xcBFacts(), legal: true,
			q:        match.Question{Selector: "s", Text: "I agree to the privacy policy", Kind: match.KindCheckbox, Options: xcBOpts("I agree")},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailMarketingOptIn},

		// XC-059
		{name: "XC-059 acknowledge the privacy notice with the setting on", sets: src, facts: xcBFacts(), legal: true,
			q:     one("Please review and acknowledge Example Corp's Job Applicant Privacy Notice", "I acknowledge that I have read the Job Applicant Privacy Notice"),
			entry: "privacy-policy-consent"},
		{name: "XC-059 acknowledge the privacy notice with the setting off", sets: src, facts: xcBFacts(),
			q:        one("Please review and acknowledge Example Corp's Job Applicant Privacy Notice", "I acknowledge that I have read the Job Applicant Privacy Notice"),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailLegalDeclarationOff},
		{name: "XC-059 verb after the noun", sets: src, facts: xcBFacts(), legal: true,
			q: one("Job Applicant Privacy Statement - please acknowledge", "I acknowledge"), entry: "privacy-policy-consent"},
		{name: "XC-059 verb-less stem", sets: src, facts: xcBFacts(), legal: true,
			q: one("Candidate Privacy Notice", "I acknowledge"), entry: "privacy-policy-consent"},
		{name: "XC-059 mention of privacy is not consent", sets: src, facts: xcBFacts(), legal: true,
			q: xcBText("Which team's privacy tooling have you worked on?"), notEntry: "privacy-policy-consent", wantNone: true},
		// Empower (Greenhouse, live): a required acknowledgement offered as a menu with one real option.
		{name: "XC-059 one-option privacy menu with the setting on", sets: src, facts: xcBFacts(), legal: true,
			q:     menu("Privacy policy, confidentiality & terms of application", "Yes, I have read and agree to the processing of my personal information as described below."),
			entry: "privacy-policy-consent", label: "Yes, I have read and agree to the processing of my personal information as described below."},
		{name: "XC-059 one-option consent menu with the setting on", sets: src, facts: xcBFacts(), legal: true,
			q:     menu("Consent for AI-Assisted Technology Use", "Yes, I consent to the use of AI-assisted hiring technologies, including video recording and transcription, as described in the Candidate Consent Agreement."),
			entry: "mandatory-acknowledge", label: "Yes, I consent to the use of AI-assisted hiring technologies, including video recording and transcription, as described in the Candidate Consent Agreement."},
		{name: "XC-059 one-option privacy menu with the setting off", sets: src, facts: xcBFacts(),
			q:        menu("Privacy policy, confidentiality & terms of application", "Yes, I have read and agree to the processing of my personal information as described below."),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailLegalDeclarationOff},
		// GC AI (Ashby, live): the same acknowledgement offered as one radio button.
		{name: "XC-059 one-option privacy radio with the setting on", sets: src, facts: xcBFacts(), legal: true,
			q:     match.Question{Selector: "s", Text: gcAINotice, Kind: match.KindRadioGroup, InputType: match.InputYesNo, Required: true, Options: xcBOpts("Continue")},
			entry: "privacy-policy-consent", label: "Continue"},
		{name: "XC-059 one-option privacy radio read as single select", sets: src, facts: xcBFacts(), legal: true,
			q:     match.Question{Selector: "s", Text: gcAINotice, Kind: match.KindRadioGroup, Required: true, Options: xcBOpts("Continue")},
			entry: "privacy-policy-consent", label: "Continue"},
		{name: "XC-059 one-option privacy radio with the setting off", sets: src, facts: xcBFacts(),
			q:        match.Question{Selector: "s", Text: gcAINotice, Kind: match.KindRadioGroup, Required: true, Options: xcBOpts("Continue")},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailLegalDeclarationOff},
	})
}

const gcAINotice = "By continuing, you acknowledge the following: GC AI will collect and use the personal data you provide to us as an applicant for recruitment-related reasons. For a full description of how this information will be used, shared, and protected, please see the GC AI Privacy Policy."

// iCapital (Greenhouse) and Quantcast (Ashby), live.
func TestXC_SponsorshipAnyTypeExampleListAndH1BAside(t *testing.T) {
	const (
		icapNow   = "Do you currently require any type of visa sponsorship for employment?"
		icapLater = "Do you require any type of visa sponsorship for employment in the future (i.e. student visa transfer to H1B, etc)?"
		quant     = "Excluding the transfer or extension of a currently valid H1B visa, will you now or in the future require sponsorship for employment authorization in the country in which this position is based?"
		sp        = "sponsorship-now-or-future"
	)
	listbox := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.Required = true
		return q
	}
	sets := map[string]xcBSet{sp: xcBAns("Yes")}
	us := match.Posting{Company: "iCapital", Location: "United States"}
	ct := match.Posting{Company: "Quantcast", Location: "Stamford, CT"}
	noNeed := xcBFacts()
	noNeed.RequiresSponsorshipFuture = xcBBool(false)
	h1b := xcBFacts()
	h1b.VisaStatus = "H-1B"
	unnamed := xcBFacts()
	unnamed.VisaStatus = ""
	h1bNoNeed := noNeed
	h1bNoNeed.VisaStatus = "H-1B"
	xcBRunCases(t, []xcBCase{
		{name: "any type of sponsorship now", sets: sets, facts: xcBFacts(), posting: us, q: listbox(icapNow), entry: sp, label: "No"},
		{name: "an example list naming a transfer, a recorded need", sets: sets, facts: xcBFacts(), posting: us, q: listbox(icapLater), entry: sp, label: "Yes"},
		{name: "an example list naming a transfer, no recorded need", sets: sets, facts: noNeed, posting: us, q: listbox(icapLater), wantNone: true},
		{name: "an example list that excludes", sets: sets, facts: xcBFacts(), posting: us,
			q: listbox("Do you require any type of visa sponsorship in the future (e.g. other than a transfer to H1B)?"), wantNone: true},
		{name: "an H-1B transfer set aside, an OPT status", sets: sets, facts: xcBFacts(), posting: ct, q: xcBYesNo(quant), entry: sp, label: "Yes"},
		{name: "an H-1B transfer set aside, an H-1B status", sets: sets, facts: h1b, posting: ct, q: xcBYesNo(quant), wantNone: true},
		{name: "an H-1B transfer set aside, a status naming no category", sets: sets, facts: unnamed, posting: ct, q: xcBYesNo(quant), wantNone: true},
		{name: "an H-1B transfer set aside, no need at all", sets: sets, facts: h1bNoNeed, posting: ct, q: xcBYesNo(quant), entry: sp, label: "No"},
	})
}

// iCapital (Greenhouse, live): the office list asks for the role's own location.
func TestXC_RoleLocationFromThePosting(t *testing.T) {
	const ask = "iCapital is an office-first culture, as such all employees are required to work in the office Monday-Thursday, with the flexibility to work remotely on Friday, depending on the department. Please review the job description before applying, to identify the specified location/s for the role."
	offices := []string{"Boca Raton, FL", "Boston, MA", "Greenwich, CT", "New York, NY", "Stamford, CT"}
	boxes := func(text string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindCheckbox, InputType: match.InputMultiSelect, Required: true, Options: xcBOpts(offices...)}
	}
	at := func(loc string) match.Posting { return match.Posting{Company: "iCapital", Location: loc} }
	stamford := at("Stamford, Connecticut, United States")
	xcBRunCases(t, []xcBCase{
		{name: "the posting's city and state", facts: xcBFacts(), posting: stamford, q: boxes(ask), label: "Stamford, CT"},
		{name: "a single-answer list", facts: xcBFacts(), posting: stamford, q: xcBQ(ask, match.KindSelect, offices...), label: "Stamford, CT"},
		{name: "the same city in another state", facts: xcBFacts(), posting: at("Stamford, Texas, United States"), q: boxes(ask), wantNone: true},
		{name: "a remote posting", facts: xcBFacts(), posting: at("Remote"), q: boxes(ask), wantNone: true},
		{name: "the applicant's wish", facts: xcBFacts(), posting: stamford, q: boxes("Please select your preferred location(s) for the role."), wantNone: true},
		{name: "two options naming the city on a single-answer list", facts: xcBFacts(), posting: stamford, q: xcBQ(ask, match.KindSelect, "Greenwich, CT", "Stamford, CT", "Stamford, Connecticut"), wantNone: true},
	})
}

func TestXC_RoutesMilitaryHealthAI(t *testing.T) {
	sets := map[string]xcBSet{
		"clearance-citizenship-gated": xcBWith(xcBPref("No, I am not a US Citizen"), xcBTypes("single_select", "yes_no")), "security-clearance": xcBPref("N/A"),
		"military-branch": xcBPref("Not applicable"), "military-service-status": xcBPref("I have not served"), "military-spouse": xcBAns("No"),
		"willing-to-obtain-clearance": xcBPref("No"), "healthcare-professional-license": xcBAns("No"), "has-stem-degree": xcBPref("Yes"),
		"highest-level-education": xcBPref("Master's Degree"), "under-18-work-permit": xcBWith(xcBPref("N/A"), xcBTypes("single_select", "yes_no")),
		"ai-tools-familiarity": xcBPref("Somewhat familiar"), "application-submitter-identity": xcBPref("I'm the real candidate"),
		"willing-to-travel": xcBPref("Up to 25%", "Yes"), "interview-travel-onsite": xcBPref("Yes"),
	}
	xcBRunCases(t, []xcBCase{
		// XC-060
		{name: "XC-060 US citizen with clearance as a select", sets: sets, facts: xcBFacts(),
			q: xcBQ("Are you a US citizen with the ability to obtain and maintain a Secret clearance?", match.KindSelect,
				"Yes, I am a US Citizen", "No, I am not a US Citizen"),
			entry: "clearance-citizenship-gated", label: "No, I am not a US Citizen"},
		{name: "XC-060 US citizen with clearance as yes no", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("Are you a US citizen with the ability to obtain and maintain a Secret clearance?"),
			entry: "clearance-citizenship-gated", label: "No"},
		{name: "XC-060 active clearance stays on the level entry", sets: sets, facts: xcBFacts(),
			q:     xcBQ("Do you hold an active security clearance?", match.KindSelect, "Top Secret", "Secret", "N/A"),
			entry: "security-clearance", label: "N/A"},
		{name: "XC-060 military branch", sets: sets, facts: xcBFacts(),
			q: xcBQ("Military Branch", match.KindSelect, "Army", "Navy", "Air Force", "Not applicable"), entry: "military-branch", label: "Not applicable"},
		{name: "XC-060 served in the armed forces", sets: sets, facts: xcBFacts(),
			q: xcBQ("Have you served in the U.S. Armed Forces and/or are you a spouse?", match.KindSelect,
				"I am a veteran", "I am a military spouse", "I have not served"),
			entry: "military-service-status", label: "I have not served"},
		{name: "XC-060 reserve or national guard", sets: sets, facts: xcBFacts(),
			q:     xcBQ("Are you currently a member of the National Guard?", match.KindSelect, "Reserve", "National Guard", "I have not served"),
			entry: "military-service-status", label: "I have not served"},
		{name: "XC-060 spouse of someone who served", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Are you the spouse of someone who served in the military?"), entry: "military-spouse", label: "No"},
		{name: "XC-060 clearance with multiple customers", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("Are you willing to obtain a clearance to work with multiple government customers?"),
			entry: "willing-to-obtain-clearance", label: "No"},

		// XC-061
		{name: "XC-061 healthcare license", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("Are you a physician, physician assistant, nurse practitioner or other healthcare professional with a current US license?"),
			entry: "healthcare-professional-license", label: "No"},
		{name: "XC-061 degree list with periods in PhD", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("Do you have a Bachelor's, Master's, or Ph.D. in Computer Science, Data Science, Mathematics, Statistics, or a related quantitative field?"),
			entry: "has-stem-degree", label: "Yes"},
		{name: "XC-061 highest education is not the STEM gate", sets: sets, facts: xcBFacts(),
			q:        xcBQ("What is your highest level of education?", match.KindSelect, "High School", "Bachelor's Degree", "Master's Degree", "Doctorate"),
			notEntry: "has-stem-degree", label: "Master's Degree"},
		{name: "XC-061 minor work permit", sets: sets, facts: xcBFacts(),
			q:     xcBQ("If you are under 18, you must furnish a work permit. Do you have one?", match.KindSelect, "N/A", "Yes", "No"),
			entry: "under-18-work-permit", label: "N/A"},

		// XC-062
		{name: "XC-062 AI familiarity", sets: sets, facts: xcBFacts(),
			q:     xcBQ("How would you describe your familiarity with AI tools?", match.KindSelect, "Not familiar", "Somewhat familiar", "Very familiar"),
			entry: "ai-tools-familiarity", label: "Somewhat familiar"},
		{name: "XC-062 completed by AI is not the project count", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Was any part of this application completed by AI?"), wantNone: true},
		{name: "XC-062 who is submitting", sets: sets, facts: xcBFacts(),
			q:     xcBQ("Who is submitting this application?", match.KindRadioGroup, "I'm the real candidate", "An automated system or AI"),
			entry: "application-submitter-identity", label: "I'm the real candidate"},

		// XC-063
		{name: "XC-063 travel if required", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Can you travel if the job requires it?"), entry: "willing-to-travel", label: "Yes"},
		{name: "XC-063 departmental meetings", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Are you willing to travel into Example City for departmental meetings?"), entry: "willing-to-travel", label: "Yes"},
		{name: "XC-063 travel percentage picker", sets: sets, facts: xcBFacts(),
			q:     xcBQ("What is your travel availability?", match.KindSelect, "Up to 10%", "Up to 25%", "Up to 50%", "More than 50%"),
			entry: "willing-to-travel", label: "Up to 25%"},
		{name: "XC-063 one-off interview trip", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("If selected as a finalist, could you travel to Example State for an in-person interview with 2 weeks notice?"),
			entry: "interview-travel-onsite", label: "Yes"},
	})
}

// UT Austin (Workday, live): a Yes/No that asks for the agreement itself is
// given by its Yes, never read as a country question for its "nation".
func TestXC_AgreementAskedAsAQuestion(t *testing.T) {
	const travel = "Do you agree to notify the university of any future personal travel to a foreign-adversary nation and submit a post-travel briefing?"
	menu := func(text string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindCustomListbox, Required: true, Options: xcBOpts("Yes", "No")}
	}
	xcBRunCases(t, []xcBCase{
		{name: "setting on", facts: xcBFacts(), legal: true, q: menu(travel), entry: "mandatory-acknowledge", label: "Yes"},
		{name: "setting off", facts: xcBFacts(), q: menu(travel), wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailLegalDeclarationOff},
		// Knit (Greenhouse, live): consent asked as a question, given by its Yes.
		{name: "consent asked", facts: xcBFacts(), legal: true, q: menu("Do you consent to Knit's Privacy Policy?"), entry: "privacy-policy-consent", label: "Yes"},
		// P&G: a list of expectations closing with "I acknowledge ...".
		{name: "a list closing with the acknowledgement", facts: xcBFacts(), legal: true,
			q:     menu("Respect the integrity of the selection process and do not share the assessment content. I acknowledge I will adhere to these expectations throughout the application process."),
			entry: "mandatory-acknowledge", label: "Yes"},
	})
}

// Q2 (Workday, live): every statement is one of employment by the audit firm
// it names, answered from the work history.
func TestXC_EmploymentStatementsAnsweredFromTheHistory(t *testing.T) {
	const q2 = "Do either of the following statements apply to you? I am currently employed by Ernst & Young at the partner level or above. I am/was employed by Ernst & Young at some point in the last 7 years and directly supported Q2's account."
	ey := xcBFacts()
	ey.Employment = []match.EmploymentFact{{Employer: "EY", Start: "2021-01", End: "2023-06"}}
	xcBRunCases(t, []xcBCase{
		{name: "no employment by the firm", facts: xcBFacts(), q: xcBYesNo(q2), entry: "employer-history", label: "No"},
		{name: "the firm under another name, a question limited to a window", facts: ey, q: xcBYesNo(q2), wantNone: true, reason: match.ReasonAmbiguous},
	})
}

// Zocdoc (Greenhouse, live): the sponsorship question's note says when to
// answer Yes ("Write "Yes" if you require any immigration or work permit/EAD
// application or renewal - sponsored by an employer, university or otherwise
// ..."); the note widens Yes and is no sensitive category.
func TestXC_SponsorshipWithANoteOnWhenToAnswerYes(t *testing.T) {
	q := xcBQ(`Do you now or in the future require sponsorship for employment visa status? (Write "Yes" if you require any immigration or work permit/EAD application or renewal - sponsored by an employer, university or otherwise - either now or at some point in the future)`, match.KindSelect, "Yes", "No")
	q.Required = true
	sp := "sponsorship-now-or-future"
	sets := map[string]xcBSet{sp: xcBAns("Yes")}
	ny := match.Posting{Company: "Zocdoc", Location: "New York, NY"}
	turned := xcBQ(`Do you now or in the future require sponsorship for employment visa status? (Write "Yes" only if you require a visa renewal)`, match.KindSelect, "Yes", "No")
	xcBRunCases(t, []xcBCase{
		{name: "a recorded need", sets: sets, facts: xcBFacts(), posting: ny, q: q, entry: sp, label: "Yes"},
		{name: "a note that narrows Yes", sets: sets, facts: xcBFacts(), posting: ny, q: turned, wantNone: true},
	})
}

// Contentful (Greenhouse, live): after "Are you a citizen, national or
// permanent resident of Iran, Cuba, North Korea or Syria?" a required
// follow-up asks about another citizenship and offers "Not Applicable. I am
// NOT a citizen, national, or permanent resident of Iran, Cuba, North Korea, or
// Syria"; the option's statement holds for a Chinese citizen living in the US.
func TestXC_ASanctionsFollowUpsOwnNotApplicable(t *testing.T) {
	na := "Not Applicable. I am NOT a citizen, national, or permanent resident of Iran, Cuba, North Korea, or Syria"
	q := xcBQ("Do you hold an ADDITIONAL citizenship, nationality, or permanent residency of any other country that is NOT Iran, Cuba, Syria or North Korea?", match.KindCustomListbox, "Yes", "No", na)
	q.InputType, q.Required = match.InputMultiSelect, true
	cn := xcBFacts()
	cn.Citizenships, cn.Address.Country = []string{"CN"}, "US"
	ir := cn
	ir.Citizenships = []string{"IR"}
	none := cn
	none.Citizenships = nil
	xcBRunCases(t, []xcBCase{
		{name: "a Chinese citizen living in the US", facts: cn, q: q, label: na},
		{name: "an Iranian citizen", facts: ir, q: q, wantNone: true},
		{name: "no citizenship recorded", facts: none, q: q, wantNone: true},
	})
}

// Mattermost (Greenhouse, live): "How many years of professional full-stack
// experience?" over "0-2 years", "3-4 years", ... with no years recorded for
// the skill and one year of full-time work in all: the skill's professional
// years lie between none and one, and only "0-2 years" holds that span. A menu
// that parts the span ("None" beside it), a question not about work and a total
// no bucket spans take the owner's default instead: the lowest bucket that is
// not "none", for review.
func TestXC_ProfessionalSkillYearsBoundedByTheTotal(t *testing.T) {
	menu := []string{"0-2 years", "3-4 years", "5-7 years", "8+ years"}
	q := xcBQ("How many years of professional full-stack experience?", match.KindCustomListbox, menu...)
	q.Required = true
	parted := xcBQ("How many years of professional full-stack experience?", match.KindCustomListbox, append([]string{"None"}, menu...)...)
	notWork := xcBQ("How many years of full-stack experience?", match.KindCustomListbox, menu...)
	sets := map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("1")}
	xcBRunCases(t, []xcBCase{
		{name: "one year in all", sets: sets, facts: xcBFacts(), q: q, label: "0-2 years"},
		{name: "a None option parts the span: the owner's default", sets: sets, facts: xcBFacts(), q: parted, label: "0-2 years"},
		{name: "not about work: the owner's default", sets: sets, facts: xcBFacts(), q: notWork, label: "0-2 years"},
		{name: "five years in all: the owner's default", sets: map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("5")}, facts: xcBFacts(), q: q, label: "0-2 years"},
	})
}

// Jane Street (Greenhouse): "Current degree start date" is the school's date,
// never the applicant's start in the role; the end date is the degree's end.
func TestXC_DegreeStartAndEndDate(t *testing.T) {
	sets := map[string]xcBSet{"earliest-start-date": xcBAns("2031-08-01")}
	dated := xcBText("Degree start date")
	dated.InputType = match.InputDate
	xcBRunCases(t, []xcBCase{
		{name: "start, a text box", sets: sets, facts: xcBFacts(), q: xcBText("Current degree start date"), answer: "September 2024", notEntry: "earliest-start-date"},
		{name: "end, a text box", sets: sets, facts: xcBFacts(), q: xcBText("Current degree end date"), answer: "May 2026"},
		{name: "start, a date control", sets: sets, facts: xcBFacts(), q: dated, answer: "2024-09-01", notEntry: "earliest-start-date"},
		{name: "the role's start date stays", sets: sets, facts: xcBFacts(), q: xcBText("Start date"), entry: "earliest-start-date"},
	})
}

// Socure (Ashby): a bare "WhatsApp" label over "Yes - I consent to receiving
// WhatsApp messages" and its refusal is the text-consent pair, as SMS is.
func TestXC_WhatsAppConsentPair(t *testing.T) {
	sets := map[string]xcBSet{"marketing-text-consent": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "WhatsApp", sets: sets, facts: xcBFacts(), q: xcBQ("WhatsApp", match.KindRadioGroup, "Yes - I consent to receiving WhatsApp messages", "No - I do not consent to receiving WhatsApp messages"),
			entry: "marketing-text-consent", label: "Yes - I consent to receiving WhatsApp messages"},
	})
}

// Postscript (Greenhouse): with "Asian" recorded, the option that lists Asian
// among other groups is the user's (the owner's decision); a group whose
// name only contains the word is not.
func TestXC_RecordedRaceTakesAListingOption(t *testing.T) {
	sets := map[string]xcBSet{"eeo-race": xcBPref("Asian")}
	api := "Asian or Pacific Islander (including East Asian, South Asian and Southeast Asian)"
	q := xcBQ("What Race/Ethnicity do you identify as? (select all that apply)", match.KindCheckbox,
		"American Indian or Alaska Native", api, "Black or African American", "Hispanic or Latino", "White", "I don't wish to answer")
	q.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "lists Asian", sets: sets, facts: xcBFacts(), q: q, entry: "eeo-race", label: api},
	})
}

// Block (Greenhouse): AI transcription and summary of the interview is the
// recording consent, answered Yes (the owner's decision).
func TestXC_AITranscriptionOfTheInterview(t *testing.T) {
	sets := map[string]xcBSet{"interview-recording-consent": xcBPref("Yes")}
	q := xcBQ("With your permission, we may use AI transcription software to transcribe and summarize your interview to help us capture accurate notes. Participation is entirely voluntary, and your decision will not affect the outcome of your application. You can withdraw consent at any time. Do you consent to us using AI to transcribe and summarize your interview?", match.KindCustomListbox, "Yes", "No")
	q.Required = true
	xcBRunCases(t, []xcBCase{{name: "Block", sets: sets, facts: xcBFacts(), q: q, entry: "interview-recording-consent", label: "Yes"}})
}

// Jane Street (Greenhouse): "Additional information (for source)" details how
// the applicant heard of the role: the recorded source detail, never a pitch.
func TestXC_SourceDetailsBox(t *testing.T) {
	// The user's bank: LinkedIn first and as the typed fallback.
	sets := map[string]xcBSet{"referral-source-specify": func(e *match.Entry) {
		e.Preferred, e.FallbackText, e.Placeholder = []string{"LinkedIn"}, "LinkedIn", false
	}}
	xcBRunCases(t, []xcBCase{
		{name: "for source", sets: sets, facts: xcBFacts(), q: xcBText("Additional information (for source)"), entry: "referral-source-specify", answer: "LinkedIn"},
		{name: "a blank source detail is left", sets: map[string]xcBSet{}, facts: xcBFacts(), q: xcBText("Additional information (for source)"), wantNone: true, entry: "referral-source-specify"},
	})
}

// Quora (Ashby): "Are you legally authorized to work in that country?" right
// after a country picker is about the country picked, on a posting open in
// several places; a picker of another country never reads as authorized.
func TestXC_ThatCountryAfterACountryPicker(t *testing.T) {
	// options false is the live page: a type-ahead whose options load only as
	// one types, then an optional "Other" box before the authorization ask.
	run := func(workCountry string, options bool) match.Result {
		bank := xcBBank(t, map[string]xcBSet{"work-country": xcBAns(workCountry), "work-auth-us": xcBAns("Yes")})
		m := match.New(bank, match.DefaultStages())
		var listed []string
		if options {
			listed = []string{"Canada", "Germany", "India", "United Kingdom", "United States", "Other"}
		}
		countries := xcBQ("In which of the following employment eligible countries (https://www.careers.quora.com/eligible-countries) are you seeking to work, if hired?", match.KindCustomListbox, listed...)
		other := xcBText("If you selected \"Other\", please provide additional information.")
		auth := xcBYesNo("Are you legally authorized to work in that country?")
		countries.Selector, other.Selector, auth.Selector = "#c", "#o", "#a"
		resp, err := m.Match(match.Request{Host: "jobs.ashbyhq.com", Facts: xcBFacts(), Today: xcBDay,
			Posting: match.Posting{Company: "Quora", Location: "Remote - Multiple Locations"}, Questions: []match.Question{countries, other, auth}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[2]
	}
	for _, options := range []bool{true, false} {
		if r := run("United States", options); r.ChosenLabel != "Yes" {
			t.Fatalf("options read %v, the United States picked: Yes, got %+v", options, r)
		}
		if r := run("Germany", options); r.ChosenLabel == "Yes" {
			t.Fatalf("options read %v, a country the applicant is not authorized in: never Yes, got %+v", options, r)
		}
	}
}

// Outtake (Ashby): "Contact Number" drawn as <input type="number"> takes the
// phone's digits, which such a box accepts; a text box keeps the stored form.
func TestXC_PhoneInANumberBox(t *testing.T) {
	sets := map[string]xcBSet{"phone-mobile": xcBAns("+1 843-555-0142")}
	num := xcBText("Contact Number")
	num.InputType = match.InputNumber
	xcBRunCases(t, []xcBCase{
		{name: "a number box", sets: sets, facts: xcBFacts(), q: num, entry: "phone-mobile", answer: "18435550142"},
		{name: "a text box", sets: sets, facts: xcBFacts(), q: xcBText("Contact Number"), entry: "phone-mobile", answer: "+1 843-555-0142"},
	})
}

// Fairmarkit (Greenhouse): "How often do you use AI coding agents ...?" over
// frequencies takes the one the user's own answer names ("my daily driver");
// an answer that names no frequency, or two, picks none.
func TestXC_FrequencyFromTheRecordedAnswer(t *testing.T) {
	q := xcBQ("How often do you use AI coding agents (such as Claude Code, Cursor, Codex or Copilot's agent mode) in your professional work?", match.KindCustomListbox,
		"Daily", "A few times a week", "I've tried them", "I don't use them")
	daily := map[string]xcBSet{"ai-coding-tools-daily-driver": xcBAns("Claude Code is my daily driver for repo-first coding.")}
	none := map[string]xcBSet{"ai-coding-tools-daily-driver": xcBAns("I use Claude Code for repo-first coding.")}
	both := map[string]xcBSet{"ai-coding-tools-daily-driver": xcBAns("Daily at work, and a few times a week at home.")}
	xcBRunCases(t, []xcBCase{
		{name: "daily driver", sets: daily, facts: xcBFacts(), q: q, entry: "ai-coding-tools-daily-driver", label: "Daily"},
		{name: "no frequency named", sets: none, facts: xcBFacts(), q: q, wantNone: true},
		{name: "two frequencies named", sets: both, facts: xcBFacts(), q: q, wantNone: true},
	})
}

// Lithic (Greenhouse): keeping the camera on for the whole interview is the
// user's own statement (owner decision: Yes); a question about recording the
// interview stays the recording consent's.
func TestXC_CameraOnForTheInterview(t *testing.T) {
	sets := map[string]xcBSet{"interview-camera-on": xcBAns("Yes"), "interview-recording-consent": xcBPref("Yes")}
	lithic := match.Posting{Company: "Lithic", Location: "Remote"}
	xcBRunCases(t, []xcBCase{
		{name: "keep the camera on", sets: sets, facts: xcBFacts(), posting: lithic, q: xcBYesNo("Our interviews require you to be on video. Can you confirm that you can keep your camera on for the entire duration of the interview?"), entry: "interview-camera-on", label: "Yes"},
		{name: "camera on in interviews", sets: sets, facts: xcBFacts(), posting: lithic, q: xcBYesNo("Are you comfortable having your camera on during video interviews?"), entry: "interview-camera-on", label: "Yes"},
		{name: "recording", sets: sets, facts: xcBFacts(), posting: lithic, q: xcBYesNo("Do you consent to the interview being recorded?"), notEntry: "interview-camera-on"},
	})
}

// Time zones named as alternatives, or as "a US timezone", are answered from
// the recorded zone: Yes when it is one of them, No when it is none. A US
// zone question is no metro question (route 124 had answered "Are you based in
// a US or equivalent timezone?" No for a Charleston, SC applicant).
func TestXC_LocatedInOneOfTheNamedTimeZones(t *testing.T) {
	eastern := map[string]xcBSet{"time-zone": xcBPref("Eastern Time (ET)", "EST"), "based-in-named-metro": xcBAns("No")}
	pacific := map[string]xcBSet{"time-zone": xcBPref("Pacific Time (PT)", "PST"), "based-in-named-metro": xcBAns("No")}
	q := xcBYesNo("Do you currently reside in a Central or Eastern Time Zone (U.S.)?")
	us := xcBYesNo("Are you based in a US or equivalent timezone?")
	xcBRunCases(t, []xcBCase{
		{name: "Central or Eastern, on Eastern time", sets: eastern, facts: xcBFacts(), q: q, entry: "time-zone", label: "Yes"},
		{name: "Central or Eastern, on Pacific time", sets: pacific, facts: xcBFacts(), q: q, entry: "time-zone", label: "No"},
		{name: "a US timezone", sets: eastern, facts: xcBFacts(), q: us, entry: "time-zone", label: "Yes"},
		{name: "a zone or a willingness", sets: eastern, facts: xcBFacts(), q: xcBYesNo("Are you located in EST or willing to work EST hours?"), notEntry: "time-zone"},
		// Synthesia (Ashby): the East Coast is the Eastern zone, "US" before it the country.
		{name: "US East Coast or Central, on Eastern time", sets: eastern, facts: xcBFacts(), q: xcBYesNo("Are you based within US East Coast or Central timezone?"), entry: "time-zone", label: "Yes"},
		{name: "US East Coast or Central, on Pacific time", sets: pacific, facts: xcBFacts(), q: xcBYesNo("Are you based within US East Coast or Central timezone?"), entry: "time-zone", label: "No"},
		{name: "the West Coast", sets: pacific, facts: xcBFacts(), q: xcBYesNo("Are you located in a West Coast time zone?"), entry: "time-zone", label: "Yes"},
	})
}

// Crypto.com (Lever): a list of zones under "your location and schedule
// flexibility" takes the option naming the recorded zone (owner decision: the
// East Coast option for an applicant on Eastern time), not the one that only
// aligns its hours with EST.
func TestXC_LocationAndScheduleFlexibilityList(t *testing.T) {
	sets := map[string]xcBSet{"time-zone": xcBPref("Eastern Time (ET)", "East Coast", "EST")}
	q := xcBQ("Please select the option(s) that best describes your location and schedule flexibility.", match.KindRadioGroup,
		"East Coast (EST/EDT)", "Central / Mountain (CST/MST) – I am open to shifting my hours to align with EST",
		"West Coast (PST/PDT) – I am open to shifting my hours to align with EST", "No – I am not willing or able to work outside of my local timezone's standard hours.")
	xcBRunCases(t, []xcBCase{
		{name: "East Coast", sets: sets, facts: xcBFacts(), posting: match.Posting{Company: "Crypto.com", Location: "United States"}, q: q, entry: "time-zone", label: "East Coast (EST/EDT)"},
	})
}

// Owner decision: a years question about a subject the user's information
// names but records no years for takes one year in a box and the lowest
// bucket that is not "none" in a list (Tailscale's "<1"; "Less than 2 years"
// beside "None"), filled for review. A subject the information never names
// keeps its zero (F-10).
func TestXC_UnrecordedYearsTakeTheOwnersDefault(t *testing.T) {
	f := xcBFacts()
	f.Skills = append(f.Skills, match.SkillFact{Name: "React"}, match.SkillFact{Name: "Go"}, match.SkillFact{Name: "Frontend development"})
	f.KnowledgeRead = true
	front := xcBQ("Years of working as a frontend software engineer directly supporting a production system?", match.KindCustomListbox, "<1", "1-2", "2-3", "3-4", "4-5", "5+")
	goList := xcBQ("How much professional experience do you have writing production Go?", match.KindCustomListbox, "None", "Less than 2 years", "2 to 5 years", "More than 5 years")
	xcBRunCases(t, []xcBCase{
		{name: "frontend list", facts: f, q: front, label: "<1"},
		{name: "Go list beside None", facts: f, q: goList, label: "Less than 2 years"},
		{name: "React box", facts: f, q: xcBText("How many years of experience do you have with React?"), answer: "1"},
		// A ladder of whole years: its least rung, not the open "3+".
		{name: "React ladder", facts: f, q: xcBQ("How many years of experience do you have with React?", match.KindCustomListbox, "None", "1 year", "2 years", "3+ years"), label: "1 year"},
		{name: "never named", facts: f, q: xcBText("How many years of experience do you have with Elixir?"), answer: "0"},
	})
}

// BigID (Greenhouse): "How many years of experience do you have in a
// customer-facing technical role involving product implementation or
// deployment?" asks about a kind of role, a subject of its own: never the
// total years. "In a similar role" is the posting's, and keeps the total.
func TestXC_YearsInAKindOfRole(t *testing.T) {
	total := map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("5")}
	f := xcBFacts()
	f.KnowledgeRead = true
	named := xcBFacts()
	named.KnowledgeRead = true
	named.Skills = append(named.Skills, match.SkillFact{Name: "Customer-facing solutions engineering"})
	const bigid = "How many years of experience do you have in a customer-facing technical role involving product implementation or deployment?"
	xcBRunCases(t, []xcBCase{
		{name: "a kind of role never named", sets: total, facts: f, q: xcBText(bigid), notEntry: "years-full-time-work-experience-number", answer: "0"},
		{name: "a kind of role named", sets: total, facts: named, q: xcBText(bigid), notEntry: "years-full-time-work-experience-number", answer: "1"},
		// Blumira (Greenhouse): the same kind of role asked as the years spent in it.
		{name: "years spent in a kind of role", sets: total, facts: f, notEntry: "years-full-time-work-experience-number", label: "< 1 Year",
			q: xcBQ("How many years have you spent in a customer-facing technical role?", match.KindCustomListbox, "< 1 Year", "1-3 Years", "3-5 Years", "5+ Years")},
		{name: "a similar role", sets: total, facts: f, q: xcBText("How many years of experience do you have in a similar role?"), entry: "years-full-time-work-experience-number", answer: "5"},
		{name: "the total", sets: total, facts: f, q: xcBText("How many years of experience do you have?"), entry: "years-full-time-work-experience-number", answer: "5"},
	})
}

// Babylist (Greenhouse): "currently authorized ... without needing sponsorship
// now or in the future" gives the need its own time, so the authorization's
// "currently" leaves it read as now or later: No for a need later, Yes for
// none; with no time on the need it stays Ivo's ambiguity. "The country listed
// for this role" is the role's. Backblaze (Greenhouse): sponsorship "for
// permanent employment" is the plain need.
func TestXC_AuthorizedWithoutSponsorshipNowOrLater(t *testing.T) {
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	babylist := match.Posting{Company: "Babylist", Location: "United States"}
	auth := xcBYesNo("Are you currently authorized to work for any employer in the country listed for this role, without needing sponsorship now or in the future (e.g., H-1B, work permit, or other visa/authorization extension)")
	plain := xcBYesNo("Are you currently authorized to work for any employer in the country listed for this role, without needing sponsorship now or in the future")
	backblaze := match.Posting{Company: "Backblaze", Location: "Remote - US"}
	xcBRunCases(t, []xcBCase{
		{name: "a need later", sets: sets, facts: xcBNeeds(false, true), posting: babylist, q: auth, entry: "sponsorship-now-or-future", label: "No"},
		{name: "no need", sets: sets, facts: xcBNeeds(false, false), posting: babylist, q: plain, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "no time on the need", sets: sets, facts: xcBNeeds(false, true), posting: babylist, q: xcBYesNo("Are you currently authorized to work in the United States without employer sponsorship?"), entry: "sponsorship-now-or-future", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "permanent employment", sets: sets, facts: xcBNeeds(false, true), posting: backblaze, q: xcBYesNo("Do you require sponsorship, now or in the future, for permanent employment? (e.g., H-1B visa status)?"), entry: "sponsorship-now-or-future", label: "Yes"},
	})
}

// brightwheel (Ashby): "Are you currently authorized to work in the US?" over
// "I am authorized ... without sponsorship, now or in the future", an H-1B
// transfer and "I require company sponsorship for a new visa ..." states the
// sponsorship standing in its options. A list with no option for a need only
// later gives that need the requirement option that does not say "now"; one
// that says "currently" is not it.
func TestXC_StandingListWithoutAFutureOption(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	bw := match.Posting{Company: "brightwheel", Location: "United States"}
	const none, transfer, newVisa = "I am authorized to work in the United States without sponsorship, now or in the future",
		"I am authorized to work in the United States under a current H-1B (or similar) visa and require a transfer to my next employer",
		"I require company sponsorship for a new visa to work in the United States"
	q := xcBQ("This role requires working from within the United States. Are you currently authorized to work in the US?", match.KindRadioGroup, none, transfer, newVisa)
	q.InputType = match.InputSingleSelect
	now := xcBQ("This role requires working from within the United States. Are you currently authorized to work in the US?", match.KindRadioGroup, none, "I currently require sponsorship to work in the United States")
	now.InputType = match.InputSingleSelect
	xcBRunCases(t, []xcBCase{
		{name: "a need later", sets: sets, facts: xcBNeeds(false, true), posting: bw, q: q, entry: "work-auth-and-future-sponsorship", label: newVisa},
		{name: "no need", sets: sets, facts: xcBNeeds(false, false), posting: bw, q: q, entry: "work-auth-and-future-sponsorship", label: none},
		{name: "a need now", sets: sets, facts: xcBNeeds(true, true), posting: bw, q: q, entry: "work-auth-and-future-sponsorship", label: newVisa},
		{name: "a need later is not a current one", sets: sets, facts: xcBNeeds(false, true), posting: bw, q: now, wantNone: true},
	})
}

// Basis (Lever): a multi-line box asking to "share a link to your LinkedIn
// profile (for identity verification purposes)" takes the profile link; "Please
// share your time zone." is no story about "a time", and takes the recorded
// zone; consenting to "an in-person interview or facial ID verification if
// required" is interview-identity-verification, answered from what the user
// records there.
func TestXC_LinkedInForVerificationTimeZoneAndIdentityCheck(t *testing.T) {
	zone := func(e *match.Entry) {
		e.Preferred, e.FallbackText, e.Placeholder = []string{"Eastern Time (ET)"}, "Eastern Time (ET)", false
	}
	sets := map[string]xcBSet{"linkedin-url": xcBAns("https://www.linkedin.com/in/example/"), "time-zone": zone, "interview-identity-verification": xcBAns("Yes")}
	basis := match.Posting{Company: "Basis", Location: "United States"}
	link := xcBText("Please share a link to your LinkedIn profile (for identity verification purposes).")
	link.InputType = match.InputLongText
	xcBRunCases(t, []xcBCase{
		{name: "link in a multi-line box", sets: sets, facts: xcBFacts(), posting: basis, q: link, entry: "linkedin-url", answer: "https://www.linkedin.com/in/example/"},
		{name: "time zone", sets: sets, facts: xcBFacts(), posting: basis, q: xcBText("Please share your time zone."), entry: "time-zone", answer: "Eastern Time (ET)"},
		{name: "identity check", sets: sets, facts: xcBFacts(), posting: basis, q: xcBYesNo("I consent to participate in an in-person interview or facial ID verification if required for this role."), entry: "interview-identity-verification", label: "Yes"},
		{name: "a time is still a story", sets: sets, facts: xcBFacts(), posting: basis, q: xcBText("Please share a time you failed and what you learned."), wantNone: true},
	})
}

// Babylist (Greenhouse): "Were you referred to this role by a current Babylist
// employee?" is the referral the user answered (owner decision: No).
func TestXC_ReferredToThisRoleByACurrentEmployee(t *testing.T) {
	sets := map[string]xcBSet{"referred-by-current-employee": xcBAns("No")}
	babylist := match.Posting{Company: "Babylist", Location: "United States"}
	q := xcBQ("Were you referred to this role by a current Babylist employee?", match.KindCustomListbox, "Yes", "No")
	xcBRunCases(t, []xcBCase{
		{name: "referred to this role", sets: sets, facts: xcBFacts(), posting: babylist, q: q, entry: "referred-by-current-employee", label: "No"},
		{name: "radio", sets: sets, facts: xcBFacts(), posting: babylist, q: xcBYesNo("Were you referred to this role by a current Babylist employee?"), entry: "referred-by-current-employee", label: "No"},
		{name: "plain", sets: sets, facts: xcBFacts(), posting: babylist, q: xcBYesNo("Were you referred by a current employee?"), entry: "referred-by-current-employee", label: "No"},
	})
}

// BILL (Greenhouse): "Are you currently located in, or open to relocating to,
// one BILL's remote-eligible states?" names states no profile holds; it is
// remote-eligible-location, answered from what the user records there (owner
// decision: Yes).
func TestXC_LocatedInOrRelocatingToEligibleStates(t *testing.T) {
	sets := map[string]xcBSet{"remote-eligible-location": xcBAns("Yes")}
	bill := match.Posting{Company: "BILL", Location: "United States"}
	const ask = "Are you currently located in, or open to relocating to, one BILL's remote-eligible states?"
	list := xcBQ(ask, match.KindCustomListbox, "Yes, I currently reside in one of the eligible states", "No, but I am open to relocating to an eligible state", "No, I am not open to relocation")
	xcBRunCases(t, []xcBCase{
		{name: "yes or no", sets: sets, facts: xcBFacts(), posting: bill, q: xcBYesNo(ask), entry: "remote-eligible-location", label: "Yes"},
		{name: "three options", sets: sets, facts: xcBFacts(), posting: bill, q: list, entry: "remote-eligible-location", label: "Yes, I currently reside in one of the eligible states"},
		{name: "a named city stays the relocation's", sets: sets, facts: xcBFacts(), posting: bill, q: xcBYesNo("Are you currently located in, or open to relocating to, San Jose, CA?"), entry: "willing-to-relocate", wantNone: true},
	})
}

// Rocket Money (Greenhouse): "Are you interested in working from one of our
// offices?" over a regular Yes, an occasional Yes and No is the office
// attendance; the recorded five days on site meet "2+ days per week".
func TestXC_InterestedInWorkingFromOneOfOurOffices(t *testing.T) {
	sets := map[string]xcBSet{"office-attendance-ok": xcBAns("Yes"), "days-per-week-onsite": xcBPref("5")}
	rocket := match.Posting{Company: "Rocket Money", Location: "Remote (USA)"}
	const ask = "Are you interested in working from one of our offices?"
	xcBRunCases(t, []xcBCase{
		{name: "regular, occasional or no", sets: sets, facts: xcBFacts(), posting: rocket, q: xcBQ(ask, match.KindCustomListbox, "Yes - regularly (2+ days per week)", "Yes - occasionally", "No"), entry: "office-attendance-ok", label: "Yes - regularly (2+ days per week)"},
		{name: "yes or no", sets: sets, facts: xcBFacts(), posting: rocket, q: xcBYesNo(ask), entry: "office-attendance-ok", label: "Yes"},
		{name: "a maximum below the days takes the occasional Yes", sets: map[string]xcBSet{"office-attendance-ok": func(e *match.Entry) {
			xcBAns("Yes")(e)
			one, t := 1, *e.NumericThreshold
			t.MaxValue = &one
			e.NumericThreshold = &t
		}}, facts: xcBFacts(), posting: rocket, q: xcBQ(ask, match.KindCustomListbox, "Yes - regularly (2+ days per week)", "Yes - occasionally", "No"), entry: "office-attendance-ok", label: "Yes - occasionally"},
		{name: "the office follow-up stays the picker's", sets: sets, facts: xcBFacts(), posting: rocket, q: xcBQ("If you answered 'Yes' to working from one of our offices, which office are you interested in working from?", match.KindCheckbox, "New York City", "Denver", "None"), wantNone: true, entry: "preferred-work-location"},
	})
}

// NerdWallet (Ashby): "the country this role is listed in" is the posting's.
func TestXC_ResideInTheCountryThisRoleIsListedIn(t *testing.T) {
	f := xcBFacts()
	f.Address.Country = "US"
	q := xcBYesNo("Do you currently reside in the country this role is listed in?")
	xcBRunCases(t, []xcBCase{
		{name: "a US posting", facts: f, posting: match.Posting{Company: "NerdWallet", Location: "Remote, US"}, q: q, label: "Yes"},
		{name: "a posting abroad", facts: f, posting: match.Posting{Company: "NerdWallet", Location: "Toronto, Canada"}, q: q, label: "No"},
		{name: "no recorded country", facts: xcBFacts(), posting: match.Posting{Company: "NerdWallet", Location: "Remote, US"}, q: q, wantNone: true},
	})
}

// Wayve (Ashby): "the country where you're applying" is the role's.
func TestXC_EligibleInTheCountryWhereYoureApplying(t *testing.T) {
	q := xcBYesNo("Are you eligible to work in the country where you're applying?")
	xcBRunCases(t, []xcBCase{
		{name: "a US posting", sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes")}, facts: xcBFacts(), posting: match.Posting{Company: "Wayve", Location: "Sunnyvale, California USA"}, q: q, entry: "work-auth-us", label: "Yes"},
	})
}

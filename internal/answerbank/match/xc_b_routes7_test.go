package match_test

import (
	"slices"
	"testing"

	"applysmith/internal/answerbank/match"
)

// Acknowledgements of a notice as read are ticked (the owner's decision):
// Flatiron's sponsorship notice over one "Acknowledge", and Tenstorrent's
// export-control statement over Yes and No, a Yes as a ticked box would be.
// The gate reads the acknowledging sentence, so an acknowledgement that itself
// states a fact about the applicant, and a statement that no AI was used, stay
// the applicant's.
func TestXC_ANoticeAcknowledgedAsReadIsTicked(t *testing.T) {
	const flatiron = `Please note that some positions may not be authorized or eligible for sponsorship by the company. Failure to disclose sponsorship requirements may result in disqualification from the application process or withdrawal of any offer of employment. Please select "Acknowledge" below to signify that you have read and understand this notice.`
	notice := match.Question{Selector: "s", Text: flatiron, Kind: match.KindCustomListbox, InputType: match.InputSingleSelect, Required: true, Options: xcBOpts("Acknowledge")}
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	box := func(text string) match.Question {
		q := xcBQ(text, match.KindCheckbox, text)
		q.Required = true
		return q
	}
	stating := notice
	stating.Text = `Please select "Acknowledge" below to signify that you have read and understand this notice and that you will not require sponsorship.`
	xcBRunCases(t, []xcBCase{
		{name: "the sponsorship notice", facts: xcBFacts(), legal: true, q: notice, entry: "mandatory-acknowledge", label: "Acknowledge"},
		{name: "the export-control statement", facts: xcBFacts(), legal: true, q: yesNo("I have read and understand the Export Control statement included in the job description above."), label: "Yes"},
		// GHX (Greenhouse, live): what the role does not offer, acknowledged.
		{name: "the role's sponsorship policy", facts: xcBFacts(), legal: true, q: yesNo("I acknowledge that this role is not eligible for visa sponsorship."), label: "Yes"},
		{name: "an acknowledgement stating a fact", facts: xcBFacts(), legal: true, q: stating, wantNone: true},
		{name: "a certification beside the acknowledgement", facts: xcBFacts(), legal: true,
			q: box("I certify that I have never been convicted of a felony. I have read and understand the policy above."), wantNone: true},
		{name: "no AI used", facts: xcBFacts(), legal: true, q: yesNo("I confirm that I did not use AI to complete this application."), wantNone: true},
		{name: "no AI used, a box", facts: xcBFacts(), legal: true, q: box("I certify that I have not used any AI tools in preparing my answers."), wantNone: true},
	})
}

// Osano (Ashby): the smallest company worked for is the owner's standing
// answer, ten employees: the option whose range holds it, or "10" in a box.
func TestXC_SmallestCompanyWorkedForIsTen(t *testing.T) {
	const ask = "What is the smallest size company you have worked for?"
	sizes := xcBQ(ask, match.KindCustomListbox, "1–10 employees", "11–50 employees", "51–100 employees", "101–500 employees", "501–1,000 employees", "1,001+ employees", "Self-employed / Sole proprietor")
	sizes.InputType = match.InputSingleSelect
	xcBRunCases(t, []xcBCase{
		{name: "a list", facts: xcBFacts(), q: sizes, label: "1–10 employees"},
		{name: "a box", facts: xcBFacts(), q: xcBText(ask), answer: "10"},
	})
}

// Flatiron (Greenhouse): "Are you willing to answer questions to help the
// company evaluate potential sponsorship of a work visa?" asks a willingness,
// not the need: the owner's standing Yes, never the sponsorship entry's.
func TestXC_WillingToAnswerSponsorshipQuestionsIsYes(t *testing.T) {
	q := xcBQ("If yes you will require sponsorship: Are you willing to answer questions to help the company evaluate potential sponsorship of a work visa?", match.KindCustomListbox, "Yes", "No")
	q.InputType, q.Required = match.InputYesNo, true
	xcBRunCases(t, []xcBCase{
		{name: "willing", facts: xcBFacts(), q: q, label: "Yes", notEntry: "sponsorship-now-or-future"},
	})
}

// Planet (Greenhouse): a sanctions residence question announcing its list
// inline ("any of the following countries: Cuba, ...") reads the list it
// holds; an address in none of the places is No (the owner's decision: export
// control from the recorded citizenship and home). A list announced but not
// held stays left.
func TestXC_SanctionsListAnnouncedInline(t *testing.T) {
	f := xcBFacts()
	f.Citizenships = []string{"China"}
	f.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	planet := xcBQ("Do you reside in or maintain an established permanent residence in any of the following countries: Cuba, Iran, North Korea, Syria, or the following territories of Ukraine (Luhansk, Donetsk, Crimea)?", match.KindCustomListbox, "Yes", "No")
	planet.InputType, planet.Required = match.InputYesNo, true
	below := xcBQ("Do you reside in any of the following countries?", match.KindCustomListbox, "Yes", "No")
	below.InputType, below.Required = match.InputYesNo, true
	xcBRunCases(t, []xcBCase{
		{name: "the list inline", facts: f, q: planet, label: "No"},
		{name: "the list elsewhere", facts: f, q: below, wantNone: true},
	})
}

// The owner's blanks (Abridge, the Institute of Foundation Models): a blank
// Google Scholar profile or publications list answers a required question
// No, and a Yes/No question its No; an optional box stays blank; the user's
// own answer wins. A competing offer's deadline is "N/A" while
// competing-offers says No. Whether a salary range is base pay or on-target
// earnings is the salary-range-basis entry's.
func TestXC_OwnersBlanksAndOfferDeadlineAndRangeBasis(t *testing.T) {
	req := func(q match.Question) match.Question { q.Required = true; return q }
	proud := req(xcBText("What machine learning research publication are you most proud of, where you were the first author? Why was your research significant?"))
	proud.InputType = match.InputLongText
	basis := xcBQ("Does your range reflect Base Pay or On-Target Earnings", match.KindCustomListbox, "Annual Base Salary", "Annual On-Target Earnings (typical for Sales/GTM roles with commission)")
	basis.InputType = match.InputSingleSelect
	no := map[string]xcBSet{"competing-offers": xcBAns("No. I have no offers or offer deadlines at the moment.")}
	xcBRunCases(t, []xcBCase{
		{name: "a required Google Scholar box", facts: xcBFacts(), q: req(xcBText("Google Scholar/Publications")), entry: "google-scholar-url", answer: "No"},
		{name: "an optional one", facts: xcBFacts(), q: xcBText("Google Scholar URL"), wantNone: true},
		{name: "the user's own profile", sets: map[string]xcBSet{"google-scholar-url": xcBAns("https://scholar.google.com/citations?user=x")}, facts: xcBFacts(), q: xcBText("Google Scholar URL"), entry: "google-scholar-url", answer: "https://scholar.google.com/citations?user=x"},
		{name: "a first-author publication", facts: xcBFacts(), q: proud, entry: "publications", answer: "No"},
		{name: "publications, yes or no", facts: xcBFacts(), q: xcBYesNo("Do you have any published papers?"), entry: "publications", label: "No"},
		{name: "an offer deadline with no offer", sets: no, facts: xcBFacts(), q: req(xcBText("If yes, when is your offer deadline?")), entry: "competing-offer-deadline", answer: "N/A"},
		{name: "an offer deadline, offers unrecorded", facts: xcBFacts(), q: req(xcBText("If yes, when is your offer deadline?")), wantNone: true},
		{name: "base pay or OTE", sets: map[string]xcBSet{"salary-range-basis": xcBPref("Base pay", "Base salary")}, facts: xcBFacts(), q: basis, entry: "salary-range-basis", label: "Annual Base Salary"},
	})
}

// Exa (Ashby): one box asks whether sponsorship is needed, which visa, and
// when the current one expires: the user's own sponsorship statement and the
// recorded expiry dates answer it (the owner's decision); an expiry past, or
// no statement, leaves it.
func TestXC_SponsorshipVisaAndExpiryInOneBox(t *testing.T) {
	q := xcBText("Do you require Visa sponsorship to work in your selected location? If so, which one? And when does your Visa expire? We're happy to sponsor visas - if yes, let us know which visa you'd require.")
	q.Required = true
	sets := func(exp string) map[string]xcBSet {
		return map[string]xcBSet{
			"sponsorship-type-description": xcBAns("I am currently authorized to work in the U.S. on F-1 OPT and will require H-1B visa sponsorship in the future."),
			"work-authorization-expiry":    xcBAns(exp), "stem-opt-extension-expiry": xcBAns("2034-08-02"),
		}
	}
	xcBRunCases(t, []xcBCase{
		{name: "the statement and the dates", sets: sets("2032-08-02"), facts: xcBFacts(), q: q,
			answer: "I am currently authorized to work in the U.S. on F-1 OPT and will require H-1B visa sponsorship in the future. My current work authorization is valid through 2032-08-02, with a 24-month STEM OPT extension available through 2034-08-02."},
		{name: "an expiry past", sets: sets("2030-08-02"), facts: xcBFacts(), q: q, wantNone: true},
	})
}

// Planet (Greenhouse): an export-control list of the U.S. person classes, a
// national among them, closing with "None of the above", is "None of the
// above" for an applicant who needs sponsorship, never a self-identification.
func TestXC_USPersonClassesWithANational(t *testing.T) {
	q := xcBQ("For the sole purpose of ensuring compliance with U.S. laws concerning the export of controlled or protected technologies or information, I confirm I am one of the following:",
		match.KindCustomListbox, "A citizen of the United States", "A U.S. national (*)", "A lawful permanent resident of the United States (green card holder)",
		"A person admitted into the United States as an asylee or refugee", "None of the above")
	q.InputType, q.Required = match.InputSingleSelect, true
	status := func(v string) map[string]xcBSet { return map[string]xcBSet{"us-citizen-status": xcBPref(v)} }
	citizen := xcBFacts()
	citizen.RequiresSponsorshipNow, citizen.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	xcBRunCases(t, []xcBCase{
		{name: "not a U.S. person", facts: xcBFacts(), legal: true, q: q, label: "None of the above"},
		// The user's own us-citizen-status, the bank's place for the class,
		// answers the list where the sponsorship facts cannot.
		{name: "neither, recorded", sets: status("Neither"), facts: citizen, legal: true, q: q, label: "None of the above"},
		{name: "a national, recorded", sets: status("U.S. national"), facts: citizen, legal: true, q: q, label: "A U.S. national (*)"},
		{name: "a permanent resident, recorded", sets: status("Lawful permanent resident"), facts: citizen, legal: true, q: q, label: "A lawful permanent resident of the United States (green card holder)"},
		{name: "a refugee, recorded", sets: status("Refugee or asylee"), facts: citizen, legal: true, q: q, label: "A person admitted into the United States as an asylee or refugee"},
		{name: "a bare No says nothing of the classes", sets: status("No"), facts: citizen, legal: true, q: q, wantNone: true},
		{name: "a refugee is no U.S. citizen", sets: status("Refugee or asylee"), facts: citizen, q: xcBYesNo("Are you a U.S. citizen?"), label: "No"},
	})
}

// The Answer Bank page's own choice for a refugee or an asylee, saved with its
// variants, answers the U.S. person list and a citizenship Yes/No.
func TestXC_TheRefugeeOrAsyleeChoice(t *testing.T) {
	q := xcBQ("I confirm I am one of the following:", match.KindCustomListbox, "A citizen of the United States", "A U.S. national (*)",
		"A lawful permanent resident of the United States (green card holder)", "A person admitted into the United States as an asylee or refugee", "None of the above")
	q.InputType, q.Required = match.InputSingleSelect, true
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	sets := map[string]xcBSet{"us-citizen-status": xcBPref("Refugee or asylee", "Refugee", "Asylee", "Asylum granted")}
	xcBRunCases(t, []xcBCase{
		{name: "the list", sets: sets, facts: f, legal: true, q: q, label: "A person admitted into the United States as an asylee or refugee"},
		{name: "a citizenship Yes/No", sets: sets, facts: f, q: xcBYesNo("Are you a U.S. citizen?"), label: "No"},
	})
}

// Close (Ashby) and Quo (Ashby): a text box that asks U.S. work authorization
// and to explain a visa need, or whether an H-1B would need a transfer, is the
// user's own sponsorship statement; a blank statement leaves it.
func TestXC_SponsorshipStatementInATextBox(t *testing.T) {
	const statement = "I am currently authorized to work in the U.S. on F-1 OPT and will require H-1B visa sponsorship in the future."
	sets := map[string]xcBSet{"sponsorship-type-description": xcBAns(statement)}
	explain := xcBText("Can you legally work in the US? Please explain if you require a visa.")
	transfer := xcBText("If you are based in the United States, would you require support to transfer an active H-1B Visa to work at Quo?")
	// Stackline (Greenhouse): a need of sponsorship to be explained if there is one.
	needExplained := xcBText("Do you need currently or at any point in the future immigration-related support or sponsorship from Stackline? (e.g., F-1 OPT or CPT to H-1B, H-1B transfer, TN visa, or other.) If yes, please explain.")
	needOnly := xcBText("Do you need currently or at any point in the future immigration-related support or sponsorship from Stackline?")
	explain.Required, transfer.Required, needExplained.Required, needOnly.Required = true, true, true, true
	xcBRunCases(t, []xcBCase{
		{name: "work in the US, explain a visa", sets: sets, facts: xcBFacts(), q: explain, answer: statement},
		{name: "an H-1B transfer", sets: sets, facts: xcBFacts(), q: transfer, answer: statement},
		{name: "a blank statement", facts: xcBFacts(), q: explain, wantNone: true},
		{name: "a need of sponsorship explained if there is one", sets: sets, facts: xcBFacts(), q: needExplained, entry: "sponsorship-type-description", answer: statement},
		{name: "a need of sponsorship with nothing to explain", sets: sets, facts: xcBFacts(), q: needOnly, wantNone: true},
	})
}

// Guidepoint (Greenhouse): the consequence of untrue answers acknowledged over
// one "Yes" is the truth certification. Infisical (Ashby): "How did you
// discover Infisical?" over channels is the job source, whatever the GitHub
// repo its text names; LinkedIn is none of the channels, so the user's Other.
func TestXC_UntrueAnswersAcknowledgedAndHowYouDiscoveredUs(t *testing.T) {
	cert := xcBQ("If any information I have provided is untrue, or if I have concealed material information, I understand that this will constitute cause for the denial of employment or immediate dismissal.", match.KindCheckbox, "Yes")
	cert.Required = true
	discover := xcBQ("How did you discover Infisical? Many engineers discover Infisical through our open source GitHub repo ( https://github.com/Infisical/infisical ) — it's also a great way to get a sense of what we're building.",
		match.KindRadioGroup, "GitHub", "Social platforms (Hacker News, Reddit, X, etc.)", "Through a company or teammate", "Job post or careers page", "Other")
	discover.InputType, discover.Required = match.InputSingleSelect, true
	source := map[string]xcBSet{"job-source": xcBPref("LinkedIn", "LinkedIn Jobs", "Indeed", "Other")}
	xcBRunCases(t, []xcBCase{
		{name: "untrue answers acknowledged", facts: xcBFacts(), legal: true, q: cert, entry: "truth-certification", label: "Yes"},
		{name: "how you discovered us", sets: source, facts: xcBFacts(), q: discover, entry: "job-source", label: "Other"},
	})
}

// GHX (Greenhouse, live): a pre-employment statement over Yes and No opens on
// the employer's equal-opportunity notice; the notice speaks of no applicant,
// so the certification after it is the truth certification. A fact the
// applicant states after the same notice stays theirs.
func TestXC_AnEqualOpportunityNoticeBeforeTheCertification(t *testing.T) {
	const notice = "Pre-employment Statement: Applicants and Employees are evaluated without regard to mental or physical disability, gender identity, race, religion, sexual orientation, color, gender, national origin, age, marital status, military or veteran status or any other protected local, state, or federal status. "
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "the certification after the notice", facts: xcBFacts(), legal: true, entry: "truth-certification", label: "Yes",
			q: yesNo(notice + "I represent that the information I provide in this Application for Employment is complete, true and accurate, and if provide any incomplete, inaccurate or false information during the selection process, the organization may deny my application and/or terminate my employment if it discovers such information after I am hired. I authorize the organization to contact anyone that it deems appropriate to verify the information I have provided or to further investigate my background, past performance and suitability for employment. I consent to being discussed by any person contacted by the organization and waive all rights to bring any action for defamation, invasion of privacy or any similar claim against anyone who provides information to the organization with a good faith belief that the information provided is true. I understand that this Application for Employment is not an offer of employment and only a written offer signed by an authorized representative of the organization will be effective (Checking the box is equivalent to a handwritten signature.)")},
		{name: "a fact stated after the notice", facts: xcBFacts(), legal: true, wantNone: true,
			q: yesNo(notice + "I certify that I have no disability that would prevent me from performing the essential functions of this role.")},
	})
}

// Instructure (Ashby, US remote): "What is your current work authorization
// status in the country where this position is located?" over classes of
// standing splits the visa by the employers it covers; an F-1 OPT holder's EAD
// is open to any employer, an H-1B is held for one.
func TestXC_TheVisaOptionOfTheStatusScope(t *testing.T) {
	opts := []string{"Citizen", "Permanent resident / indefinite right to work", "Temporary visa with employer-specific authorization",
		"Temporary visa with open / transferable work authorization", "I am not currently authorized to work in this country"}
	q := xcBQ("What is your current work authorization status in the country where this position is located?", match.KindRadioGroup, opts...)
	q.InputType, q.Required = match.InputSingleSelect, true
	sets := map[string]xcBSet{"us-work-authorization-status": xcBPref("I am authorized to work in this country for any employer", "for any employer"),
		"current-immigration-status": xcBPref("F-1 OPT", "OPT", "F1 OPT", "Optional Practical Training", "Other")}
	h1b := xcBFacts()
	h1b.VisaStatus = "H-1B"
	posting := match.Posting{Company: "Instructure", Location: "US-REMOTE"}
	xcBRunCases(t, []xcBCase{
		{name: "OPT is open", sets: sets, facts: xcBFacts(), posting: posting, q: q, label: opts[3]},
		{name: "an H-1B is held for one employer", sets: sets, facts: h1b, posting: posting, q: q, label: opts[2]},
	})
}

// Owner decisions: Relativity's night shifts take the user's own Yes;
// Inkeep's kinds of companies, products and content take the selections the
// user's history supports; Crossnokaye's "When would you ideally be open to
// your next opportunity?" asks the notice period, so a month's notice is
// looking now, and a notice as long as the soonest later window is left.
func TestXC_NightShiftsKindsOfExperienceAndTheNextOpportunity(t *testing.T) {
	night := xcBYesNo("This position will require occasional night shifts. Are you willing and able to work night shifts as required?")
	night.Required = true
	box := func(text string, labels ...string) match.Question {
		q := xcBQ(text, match.KindCheckbox, labels...)
		q.InputType, q.Required = match.InputMultiSelect, true
		return q
	}
	companies := box("What type of companies have you worked at?", "Early Stage Startup (<20 employees)", "Startup (20-200)", "Growth Stage Startup (200+)", "Mid Enterprise", "Big Tech")
	products := box("What type of products have you worked on previously?", "B2B SaaS", "B2B Other", "Consumer", "Developer Tool", "Other")
	content := box("What type of content do you have experience creating?", "Demo videos of software products", "Tutorial-like video content for SaaS",
		"Written content for blogs or documentation", "Graphics and images for social media or blogs")
	next := box("When would you ideally be open to your next opportunity?", "Actively looking now", "Open in the next 3-5 months", "Exploring for the future")
	sets := map[string]xcBSet{
		"occasional-night-shifts":    xcBAns("Yes"),
		"company-types-worked-at":    xcBSel("Early Stage Startup (<20 employees)"),
		"product-types-worked-on":    xcBSel("Developer Tool", "B2B SaaS"),
		"content-types-created":      xcBSel("Written content for blogs or documentation"),
		"availability-notice-period": xcBPref("in 1 Month"),
	}
	long := map[string]xcBSet{"availability-notice-period": xcBPref("in 4 Months")}
	xcBRunCases(t, []xcBCase{
		{name: "night shifts", sets: sets, facts: xcBFacts(), q: night, entry: "occasional-night-shifts", label: "Yes"},
		{name: "kinds of companies", sets: sets, facts: xcBFacts(), q: companies, entry: "company-types-worked-at", label: "Early Stage Startup (<20 employees)"},
		{name: "kinds of products", sets: sets, facts: xcBFacts(), q: products, entry: "product-types-worked-on", answer: "Developer Tool, B2B SaaS"},
		{name: "kinds of content", sets: sets, facts: xcBFacts(), q: content, entry: "content-types-created", label: "Written content for blogs or documentation"},
		{name: "a month's notice is looking now", sets: sets, facts: xcBFacts(), q: next, label: "Actively looking now"},
		{name: "a notice as long as a window", sets: long, facts: xcBFacts(), q: next, wantNone: true},
	})
}

// Recharge (Ashby): an attestation not to record or transcribe interviews is
// agreed, as acknowledgements are (owner decision); the employer's promise to
// avoid disability-related inquiries in its accommodation note is no
// disclosure, while a disability the applicant states still is.
func TestXC_AnInterviewRecordingAttestationIsAgreed(t *testing.T) {
	const recharge = "Candidate Interview Recording & AI Notetaker Attestation Prohibited tech: I will not record, transcribe, stream, summarize, or otherwise capture any interview or assessment— including by AI notetaker tools, browser extensions, meeting bots, device recorders, or screen capture —unless gives prior written consent . “AI notetaker” means any software, bot, extension, or device that captures or analyzes interview audio/video or on-screen content in real time. Manual notes OK: I may take handwritten notes for personal use. Accessibility carve-out: If I need an accommodation (e.g., live captions, ASL, approved transcription), I will email recruiting@example.com before the interview so can provide an approved, compliant solution. I understand will avoid disability-related inquiries and will use the information only to coordinate the accommodation. Remedies: If reasonably believes an interview was recorded or transcribed without consent, may end the interview , disqualify me , and request deletion of any recording/transcript. If records an interview, it will notify me and obtain my consent and will handle such data per its candidate privacy notice."
	agree, disagree := "I have read and agree to these terms.", "I do not agree to these terms."
	q := xcBQ(recharge, match.KindRadioGroup, agree, disagree)
	q.InputType, q.Required = match.InputSingleSelect, true
	stating := q
	stating.Text = recharge + " I have a disability that requires live captions."
	xcBRunCases(t, []xcBCase{
		{name: "the attestation", facts: xcBFacts(), legal: true, q: q, label: agree},
		{name: "a disability stated beside it", facts: xcBFacts(), legal: true, q: stating, wantNone: true},
	})
}

// Tonal (Ashby): "the US and/or Canada" asks whether either holds, as "or"
// does; "and" asks both. Xometry (Greenhouse): a start no offered lead time
// reaches takes the menu's one "Other". Vizcom and NICE (Ashby, Greenhouse): a
// text box asking a need of sponsorship with its type, or a visa need to be
// clarified, takes the user's sponsorship statement.
func TestXC_EitherCountryAnOtherStartAndTheKindOfVisa(t *testing.T) {
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindRadioGroup, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	authorized := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	sf := match.Posting{Company: "Tonal", Location: "San Francisco, CA"}
	start := map[string]xcBSet{"earliest-start-date": xcBAns("2031-06-20")}
	soon := xcBQ("How soon would you be available to start?", match.KindCustomListbox, "Immediately", "2 Weeks", "Other")
	soon.InputType, soon.Required = match.InputSingleSelect, true
	untimed := xcBQ("How soon would you be available to start?", match.KindCustomListbox, "Yes", "Other")
	untimed.InputType, untimed.Required = match.InputSingleSelect, true
	const statement = "I am currently authorized to work in the U.S. on F-1 OPT and will require H-1B visa sponsorship in the future."
	stated := map[string]xcBSet{"sponsorship-type-description": xcBAns(statement)}
	kind := xcBText("Do you now or will you in the future require Visa sponsorship, and if so which type?")
	clarify := xcBText("Would you need a visa / work permit for the role in the location you are applying for? Please clarify")
	kind.Required, clarify.Required = true, true
	xcBRunCases(t, []xcBCase{
		{name: "the US and/or Canada", sets: authorized, facts: xcBFacts(), posting: sf, q: yesNo("Are you legally authorized to work in the US and/or Canada?"), entry: "work-auth-us", label: "Yes"},
		{name: "the US and Canada", sets: authorized, facts: xcBFacts(), posting: sf, q: yesNo("Are you legally authorized to work in the US and Canada?"), wantNone: true},
		{name: "a start past every lead time", sets: start, facts: xcBFacts(), q: soon, entry: "full-time-start-availability", label: "Other"},
		{name: "no lead time offered", sets: start, facts: xcBFacts(), q: untimed, wantNone: true},
		{name: "a need of sponsorship and its type", sets: stated, facts: xcBFacts(), q: kind, answer: statement},
		{name: "a visa need to clarify", sets: stated, facts: xcBFacts(), posting: match.Posting{Location: "USA - Remote"}, q: clarify, answer: statement},
	})
}

// ClassPass (Greenhouse): "the country where you will perform the duties of
// this job" is the posting's country. Fleet (Greenhouse): "visa sponsorship or
// verification" is answered by a recorded need, and left without one. Armada
// (Greenhouse): a parenthesis defining "without restriction" is read past.
// Sim (Ashby): whether working in California needs any visa or work
// authorization is the applicant's standing: Yes on a visa, No as a citizen.
func TestXC_TheDutiesCountryAVerificationADefinitionAndAVisaAtAll(t *testing.T) {
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	us := match.Posting{Location: "United States"}
	noNeed := xcBFacts()
	noNeed.RequiresSponsorshipFuture = xcBBool(false)
	citizen := xcBFacts()
	citizen.VisaStatus = "citizen"
	unrestricted := map[string]xcBSet{"unrestricted-right-to-work-us": xcBAns("No")}
	const fleet = "Do you now, or will you in the future, require visa sponsorship or verification to work legally for our company in the United States?"
	const sim = "Do you require any visas or work authorizations to work in California?"
	xcBRunCases(t, []xcBCase{
		{name: "the country of the job's duties", facts: xcBFacts(), posting: us,
			q: yesNo("Will you now, or in the future, require visa sponsorship to work legally in the country where you will perform the duties of this job?"), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "sponsorship or verification, a need", facts: xcBFacts(), q: yesNo(fleet), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "sponsorship or verification, no need", facts: noNeed, q: yesNo(fleet), wantNone: true},
		{name: "without restriction defined", sets: unrestricted, facts: xcBFacts(),
			q:     yesNo("Are you currently authorized to work in the United States for any employer without restriction? (“Without restriction” means not tied to a specific employer and not dependent on a pending or future government filing.)"),
			entry: "unrestricted-right-to-work-us", label: "No"},
		{name: "a visa at all, on a visa", facts: xcBFacts(), q: yesNo(sim), label: "Yes"},
		{name: "a visa at all, a citizen", facts: citizen, q: yesNo(sim), label: "No"},
		{name: "a visa at all, outside the United States", facts: xcBFacts(), q: yesNo("Do you require any visas or work authorizations to work in Ontario?"), wantNone: true},
	})
}

// Sigma (Greenhouse): "Would you require a new H-1B sponsorship to work with
// us?" is Yes for a student status that needs sponsorship, No for an H-1B
// holder (a transfer) or no need. Sift (Ashby): eligibility under a policy that
// sponsors no new H-1B is the opposite.
func TestXC_ANewH1BNeededOrRuledOut(t *testing.T) {
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	h1b := xcBFacts()
	h1b.VisaStatus = "H-1B"
	none := xcBFacts()
	none.RequiresSponsorshipFuture = xcBBool(false)
	unknown := xcBFacts()
	unknown.RequiresSponsorshipNow, unknown.RequiresSponsorshipFuture = nil, nil
	sigma := yesNo("Would you require a new H-1B sponsorship to work with us?")
	sift := yesNo("Sift is unable to sponsor candidates who require new/initial H-1B visa sponsorship for this role. We can consider candidates who already hold H-1B status. Based on this policy, are you eligible for hire for this position?")
	xcBRunCases(t, []xcBCase{
		{name: "a new H-1B, a student status", facts: xcBFacts(), q: sigma, label: "Yes"},
		{name: "a new H-1B, an H-1B holder", facts: h1b, q: sigma, label: "No"},
		{name: "a new H-1B, no need", facts: none, q: sigma, label: "No"},
		{name: "a new H-1B, an unrecorded need", facts: unknown, q: sigma, wantNone: true},
		{name: "eligible under the policy, a student status", facts: xcBFacts(), q: sift, label: "No"},
		{name: "eligible under the policy, an H-1B holder", facts: h1b, q: sift, label: "Yes"},
	})
}

// Thinking Machines (Ashby): "Current Role Title Please enter your current
// title at your current employer. If you are not currently employed ..." asks
// for a title: the stored Yes of being employed is never typed as one.
func TestXC_ATitleAskedIsNeverABareYes(t *testing.T) {
	const ask = `Current Role Title Please enter your current title at your current employer. If you are not currently employed (or in school etc) please enter "none" and feel free to enter previous roles in the field`
	sets := map[string]xcBSet{"currently-employed": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "a title", sets: sets, facts: xcBFacts(), q: xcBText(ask), wantNone: true},
		{name: "asked as a yes or no", sets: sets, facts: xcBFacts(), q: xcBText("Are you currently employed?"), entry: "currently-employed", answer: "Yes"},
	})
}

// Energy Solutions (Greenhouse): `Please type "I understand" in the text box
// below.` after a notice is typed as asked, as an acknowledgement is ticked
// (the owner's decision), never with the setting off nor under a statement of
// how the application was made. "Do you have hands-on experience applying
// ...?" over years is a years question: the lowest bucket for an experience
// the record does not hold (the owner's default). "Do you have intermediate
// proficiency using spreadsheets (such as Microsoft Excel) ...?" is a skill
// screen the profile's Excel answers.
func TestXC_ATypedUnderstandingYearsOfAnExperienceAndAProficiency(t *testing.T) {
	const understand = `I understand the use of artificial intelligence (AI) during any part of the interview process is not permitted and will result in the immediate disqualification as a job applicant. Please type "I understand" in the text box below.`
	box := func(text string) match.Question {
		q := xcBText(text)
		q.Required = true
		return q
	}
	years := xcBQ("Do you have hands-on experience applying engineering principles and established methodologies to perform technical analyses and energy, demand, or equipment performance calculations?", match.KindCheckbox, "0-1", "1-2", "2+")
	years.InputType, years.Required = match.InputSingleSelect, true
	excel := xcBQ("Do you have intermediate proficiency using spreadsheets (such as Microsoft Excel), databases, or engineering tools to support technical work and data analysis?", match.KindCustomListbox, "Yes", "No")
	excel.InputType, excel.Required = match.InputYesNo, true
	four := 4.0
	withExcel := xcBFacts()
	withExcel.Skills = append(withExcel.Skills, match.SkillFact{Name: "Microsoft Excel", Years: &four})
	xcBRunCases(t, []xcBCase{
		{name: "typed as asked", facts: xcBFacts(), legal: true, q: box(understand), answer: "I understand"},
		{name: "the setting off", facts: xcBFacts(), q: box(understand), wantNone: true},
		{name: "a statement of no AI used", facts: xcBFacts(), legal: true, q: box(`I did not use AI to complete this application. Please type "I agree" below.`), wantNone: true},
		{name: "years of an unrecorded experience", facts: xcBFacts(), q: years, label: "0-1"},
		{name: "a proficiency the profile holds", facts: withExcel, q: excel, entry: "skill-experience-knowledge-gate", label: "Yes"},
	})
}

// Flex (Greenhouse): attending the in-person onboarding at the HQ in the
// first week is the office attendance the applicant accepts. "Does your
// salary fall within our estimated range for this role?" is the posted
// range's comfort, and the required "If not, please specify your base salary
// request." under its Yes does not apply: "N/A".
func TestXC_OnboardingAtTheHQAndASalaryWithinTheRange(t *testing.T) {
	yesNo := func(text string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, "Yes", "No")
		q.InputType, q.Required = match.InputYesNo, true
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "onboarding", sets: map[string]xcBSet{"office-attendance-ok": xcBAns("Yes")}, facts: xcBFacts(), q: yesNo("Will you be able to attend Flex's in-person New Hire Onboarding at our NYC HQ during the Monday–Wednesday of your first week? (expenses covered per Flex's travel policy)"), entry: "office-attendance-ok", label: "Yes"},
	})
	bank := xcBBank(t, map[string]xcBSet{"comfortable-with-posted-range": xcBAns("Yes")})
	m := match.New(bank, match.DefaultStages())
	slot := xcBText("If not, please specify your base salary request.")
	slot.Required = true
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay,
		Questions: []match.Question{yesNo("Does your salary fall within our estimated range for this role?"), slot}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.EntryID != "comfortable-with-posted-range" || r.ChosenLabel != "Yes" {
		t.Fatalf("the range: %+v", r)
	}
	if r := resp.Results[1]; r.Outcome == match.OutcomeAbstained || r.Answer != "N/A" {
		t.Fatalf("the slot under a Yes: %+v", r)
	}
}

// Realm (Ashby): "Do you now or in the future require sponsorship for
// employment visa status to work in the United States?" over a need never,
// only later ("may require it in the future"), a transfer and a new
// sponsorship states the standing in its options: the later need on F-1 OPT,
// none for a citizen.
func TestXC_SponsorshipNeverLaterATransferOrNew(t *testing.T) {
	q := xcBQ("Do you now or in the future require sponsorship for employment visa status to work in the United States?", match.KindRadioGroup,
		"No, I do not require sponsorship now or in the future",
		"No, I do not require sponsorship now, but may require it in the future",
		"Yes, I currently hold a visa status that would require a transfer to a new employer",
		"Yes, I would require new sponsorship")
	q.InputType, q.Required = match.InputSingleSelect, true
	citizen := xcBFacts()
	citizen.VisaStatus, citizen.RequiresSponsorshipFuture = "citizen", xcBBool(false)
	xcBRunCases(t, []xcBCase{
		{name: "F-1 OPT", facts: xcBFacts(), q: q, label: "No, I do not require sponsorship now, but may require it in the future"},
		{name: "a citizen", facts: citizen, q: q, label: "No, I do not require sponsorship now or in the future"},
	})
}

// Turbo AI (Ashby): "Did you earn your undergraduate bachelor's degree from a
// college or university located in the United States?" asks where the school
// is, never where the applicant lives. "What year did you graduate with your
// bachelor's degree?" is the bachelor entry's year, not the most recent
// degree's; the verb "graduate" names no graduate degree.
func TestXC_TheBachelorsSchoolAndYear(t *testing.T) {
	facts := xcBFacts()
	facts.Education = []match.EducationFact{
		{School: "Example University", Degree: "Bachelor of Science (B.S)", Field: "Applied Mathematics", Start: "2020-09", End: "2024-06"},
		{School: "Example State University", Degree: "Master of Science", Field: "Computer Science", Start: "2024-08", End: "2026-05"},
	}
	facts.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	located := xcBQ("Did you earn your undergraduate bachelor's degree from a college or university located in the United States?", match.KindCustomListbox, "Yes", "No")
	located.InputType, located.Required = match.InputYesNo, true
	lives := map[string]xcBSet{"currently-living-us-or-canada": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "the school's country", sets: lives, facts: facts, q: located, wantNone: true},
		{name: "living in the US", sets: lives, facts: facts, q: xcBYesNo("Are you currently living in the United States?"), label: "Yes"},
		{name: "the bachelor's year", facts: facts, q: xcBText("What year did you graduate with your bachelor’s degree?"), answer: "2024"},
		{name: "the master's year", facts: facts, q: xcBText("What year did you complete your master's degree?"), answer: "2026"},
		{name: "unscoped", facts: facts, q: xcBText("What year did you graduate?"), answer: "2026"},
	})
}

// AHEAD (Lever): "What % of travel are you open to?" in a text box is the
// travel ceiling the profile stamps.
func TestXC_AShareOfTravelInABox(t *testing.T) {
	sets := map[string]xcBSet{"willing-to-travel": xcBPref("Up to 25%", "Occasionally", "Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "a share of travel", sets: sets, facts: xcBFacts(), q: xcBText("What % of travel are you open to?"), entry: "willing-to-travel", answer: "Up to 25%"},
	})
}

// Fill-only runs on a697ed2a. Clipbook (Ashby): the Yes "eligible for
// OPT/STEM OPT" names the recorded OPT beside STEM OPT, so it is the user's
// Yes whether or not the STEM extension is recorded. AHEAD (Lever): "require
// AHEAD to sponsor you" names the employer the board's slug "thinkahead"
// squashes. PolicyMe (Lever): "How long have you been working with React.js?"
// asks the skill's years; an unrecorded skill takes the owner's year, with its
// unit since the question names none.
func TestXC_AnOptAndStemOptYesTheEmployerInItsSlugAndYearsAskedAsHowLong(t *testing.T) {
	clip := xcBQ("Will you now or in the future require visa sponsorship?", match.KindRadioGroup,
		"No, am permanently authorized to work for any employer in the United States", "Yes, But i am eligible for OPT/STEM OPT",
		"Yes, But i currently have a valid H1B Visa", "Yes, But i am eligible for a TN Visa", "Yes, Other")
	clip.InputType = match.InputSingleSelect
	const optYes = "Yes, But i am eligible for OPT/STEM OPT"
	sponsor := func(ext string) map[string]xcBSet {
		sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
		if ext != "" {
			sets["stem-opt-24-month-extension"] = xcBAns(ext)
		}
		return sets
	}
	react := xcBText("How long have you been working with React.js?")
	react.InputType = match.InputLongText
	xcBRunCases(t, []xcBCase{
		{name: "OPT/STEM OPT, the extension unrecorded", sets: sponsor(""), facts: xcBFacts(), q: clip, entry: "sponsorship-now-or-future", label: optYes},
		{name: "OPT/STEM OPT, no extension", sets: sponsor("No"), facts: xcBFacts(), q: clip, entry: "sponsorship-now-or-future", label: optYes},
		{name: "OPT/STEM OPT, in the extension", sets: sponsor("Yes"), facts: xcBFacts(), q: clip, entry: "sponsorship-now-or-future", label: optYes},
		{name: "the employer in its slug", sets: sponsor(""), facts: xcBFacts(), posting: match.Posting{Company: "thinkahead"},
			q: xcBText("Will you now or in the future require AHEAD to sponsor you for employment authorization in the United States?"), entry: "sponsorship-now-or-future", answer: "Yes"},
		{name: "years asked as how long", facts: xcBFacts(), q: react, answer: "1 year"},
		{name: "a recorded skill", facts: xcBFacts(), q: xcBText("How long have you been using Python?"), answer: "3 years"},
		{name: "working in a country", facts: xcBFacts(), q: xcBText("How long have you been working in the United States?"), wantNone: true},
	})
}

// AHEAD (Lever, fill-only on a697ed2a): "Provide All Post-Secondary Education
// Attained - Formatted as: College Name; Degree Obtained ..." lists every
// ended education in the asked format, one per line; one still in progress is
// not attained, and a one-line box takes one education only.
func TestXC_EducationAttainedInTheAskedFormat(t *testing.T) {
	const ask = "Provide All Post-Secondary Education Attained - Formatted as: College Name; Degree Obtained (e.g.: University of Somewhere; Bachelor of Science). Please include only educational programs from which you graduated.  **Any additional information provided, such as dates/year of graduation, will be disregarded.**"
	box := xcBText(ask)
	box.InputType = match.InputLongText
	line := xcBText(ask)
	line.InputType = match.InputText
	two := xcBFacts()
	two.Education = append(two.Education, match.EducationFact{School: "Sample College", Degree: "Bachelor of Science", Field: "Mathematics", Start: "2020-09", End: "2024-05"})
	inProgress := xcBFacts()
	inProgress.Education[0].End = "2032-05"
	xcBRunCases(t, []xcBCase{
		{name: "two ended", facts: two, q: box, answer: "Example State University; Master of Science\nSample College; Bachelor of Science"},
		{name: "one in progress", facts: func() match.Facts { f := inProgress; f.Education = append(f.Education, two.Education[1]); return f }(), q: box, answer: "Sample College; Bachelor of Science"},
		{name: "none ended", facts: inProgress, q: box, wantNone: true},
		{name: "two in one line", facts: two, q: line, wantNone: true},
	})
}

// Fill-only runs on d077d7da. Kobie (Lever): "require Kobie to sponsor an
// employment-based immigration case ... (for example, an H-1B visa or other
// work authorization sponsorship)" is the plain sponsorship question, and a
// read check over bare options picks the option at the named place, never an
// option that says something. Verana Health (Greenhouse): a closed list that
// reports no options under "... sponsorship for employment-based immigration
// status?" is the sponsorship, not the status: its abstain names the
// sponsorship entry, whose answer computer use writes once it opens the list
// (route 94 had named the status, "F-1 OPT"); "CA" and "Other" under "What US
// State do you currently reside in?" take "Other" for South Carolina.
// Workstream (Greenhouse): collaborating with cross-functional teams is a duty
// of the role (the owner's Yes).
func TestXC_AnEmploymentBasedCaseAReadCheckAStatusWordOneStateAndCollaboration(t *testing.T) {
	sponsor := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	kobie := xcBQ("Will you require Kobie to sponsor an employment-based immigration case now or at any time in the future in order to work in the United States (for example, an H-1B visa or other work authorization sponsorship)?", match.KindSelect, "Yes", "No")
	kobie.InputType = match.InputYesNo
	read := func(labels ...string) match.Question {
		q := xcBQ("To confirm you carefully read instructions, please select the SECOND option below", match.KindRadioGroup, labels...)
		q.InputType = match.InputSingleSelect
		return q
	}
	verana := xcBQ("Do you now, or will you in the future, require Verana Health sponsorship for employment-based immigration status?", match.KindCustomListbox)
	verana.InputType = match.InputSingleSelect
	state := xcBQ("What US State do you currently reside in?", match.KindCustomListbox, "CA", "Other")
	state.InputType = match.InputSingleSelect
	sc := map[string]xcBSet{"address-state": xcBPref(match.StateChoices("SC")...)}
	collab := xcBQ("Are you comfortable regularly communicating and collaborating with cross-functional teams, including non-technical stakeholders such as GTM, Product, and Operations?", match.KindCustomListbox, "Yes", "No")
	collab.InputType = match.InputYesNo
	xcBRunCases(t, []xcBCase{
		{name: "an employment-based case", sets: sponsor, facts: xcBFacts(), posting: match.Posting{Company: "kobie"}, q: kobie, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a read check", facts: xcBFacts(), q: read("OPTION 1", "OPTION 2", "OPTION 3", "OPTION 4"), label: "OPTION 2"},
		{name: "a read check over statements", facts: xcBFacts(), q: read("I agree", "I am a bot", "I disagree"), wantNone: true},
		{name: "a status word, no options", sets: sponsor, facts: xcBFacts(), posting: match.Posting{Company: "veranahealth"}, q: verana, entry: "sponsorship-now-or-future", wantNone: true},
		{name: "one state and Other", sets: sc, facts: xcBFacts(), q: state, entry: "address-state", label: "Other"},
		{name: "one state and Other, no state asked", sets: sc, facts: xcBFacts(), q: xcBQ("Location", match.KindCustomListbox, "CA", "Other"), wantNone: true},
		{name: "cross-functional collaboration", sets: map[string]xcBSet{"comfortable-with-role-duties": xcBAns("Yes")}, facts: xcBFacts(), q: collab, entry: "comfortable-with-role-duties", label: "Yes"},
	})
}

// Aptos Labs (Greenhouse): "Are you authorized to work in the location you
// are applying for?" over Yes and No is the work authorization for the
// posting's place, Palo Alto in the United States. Verana Health
// (Greenhouse): offices written "New York NY, San Francisco CA" are in the
// United States, so the sponsorship question is answered.
func TestXC_AuthorizedInTheLocationAppliedForAndOfficesWithoutCommas(t *testing.T) {
	q := xcBQ("Are you authorized to work in the location you are applying for?", match.KindCustomListbox, "Yes", "No")
	q.InputType = match.InputYesNo
	verana := xcBQ("Do you now, or will you in the future, require Verana Health sponsorship for employment-based immigration status?", match.KindCustomListbox, "Yes", "No")
	verana.InputType = match.InputYesNo
	xcBRunCases(t, []xcBCase{
		{name: "the location applied for", sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes")}, facts: xcBFacts(), posting: match.Posting{Company: "Aptos", Location: "Palo Alto, California"}, q: q, entry: "work-auth-us", label: "Yes"},
		{name: "offices without commas", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}, facts: xcBFacts(), posting: match.Posting{Company: "Verana Health", Location: "New York NY, San Francisco CA"}, q: verana, entry: "sponsorship-now-or-future", label: "Yes"},
	})
}

// 8th Light (Greenhouse, live): twenty states, "My state is not listed here"
// and "I live outside of the U.S." for an applicant in South Carolina; the
// list's own words for a state it lacks are its catch-all.
func TestXC_AStateListSaysTheStateIsNotListed(t *testing.T) {
	states := []string{"California", "Colorado", "Florida", "Georgia", "Illinois", "Indiana", "Iowa", "Maryland", "Massachusetts", "Michigan",
		"New Jersey", "New York", "North Carolina", "Ohio", "Oregon", "Pennsylvania", "Texas", "Virginia", "Washington", "Wisconsin"}
	q := func(extra ...string) match.Question {
		q := xcBQ("Which U.S. state do you currently reside in?", match.KindCustomListbox, append(slices.Clone(states), extra...)...)
		q.InputType = match.InputSingleSelect
		return q
	}
	sc := map[string]xcBSet{"address-state": xcBPref(match.StateChoices("SC")...)}
	xcBRunCases(t, []xcBCase{
		{name: "8th Light", sets: sc, facts: xcBFacts(), q: q("My state is not listed here", "I live outside of the U.S."), entry: "address-state", label: "My state is not listed here"},
		{name: "the state is listed", sets: sc, facts: xcBFacts(), q: q("South Carolina", "My state is not listed here"), entry: "address-state", label: "South Carolina"},
		{name: "only outside the US", sets: sc, facts: xcBFacts(), q: q("I live outside of the U.S."), entry: "address-state", wantNone: true},
	})
}

// FutureFit AI (Ashby, live), a "Remote (North America)" posting for an
// applicant in Charleston, SC: authorization in "Canada or the United States"
// is the home country's, for review; a sponsorship "or an employer-assisted
// LMIA" is one list, which a recorded need answers; EST/EDT business hours
// take the working-hours entry's overlap with any U.S. time zone.
func TestXC_EitherNorthAmericanCountryAnLMIAAndEasternBusinessHours(t *testing.T) {
	us := xcBFacts()
	us.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	ca := xcBFacts()
	ca.Address = match.AddressFact{City: "Toronto", Region: "ON", Country: "CA"}
	remote := match.Posting{Company: "FutureFit AI", Location: "Remote (North America)"}
	const own = "Flexible. I am based in Charleston, SC, in Eastern Time (ET), and usually work 9 AM to 6 PM ET; I am happy to shift my hours to overlap with the team's core hours in any U.S. time zone."
	auth := xcBYesNo("Are you legally eligible to work in Canada or the United States?")
	lmia := xcBYesNo("Will you now or in the future require visa sponsorship or an employer-assisted LMIA to work in this role?")
	hours := xcBYesNo("Are you able to work in EST/ EDT business hours?")
	xcBRunCases(t, []xcBCase{
		{name: "either country, home US", sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes")}, facts: us, posting: remote, q: auth, entry: "work-auth-us", label: "Yes"},
		{name: "either country, home Canada", sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes")}, facts: ca, posting: remote, q: auth, wantNone: true},
		{name: "sponsorship or an LMIA", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}, facts: us, posting: remote, q: lmia, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "Eastern business hours", sets: map[string]xcBSet{"working-hours": xcBAns(own)}, facts: us, posting: remote, q: hours, label: "Yes"},
		{name: "an entry silent on the hours", sets: map[string]xcBSet{"working-hours": xcBAns("9 AM to 6 PM ET.")}, facts: us, posting: remote, q: hours, wantNone: true},
	})
}

// US Mobile (Lever, New York City): the board slug "USMobile" is the company
// the questions write as "US Mobile Inc", and "lawfully" stands after "work".
func TestXC_ABoardSlugTheQuestionWritesAsWordsAndWorkingLawfullyInCanada(t *testing.T) {
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	f.Citizenships = []string{"CN"}
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "work-authorization-other-countries": xcBAns("No")}
	slug := match.Posting{Company: "USMobile", Location: "New York City"}
	xcBRunCases(t, []xcBCase{
		{name: "the US for the slug's company", sets: sets, facts: f, posting: slug,
			q: xcBYesNo("Are you authorized to work lawfully in the US for US Mobile Inc?"), entry: "work-auth-us", label: "Yes"},
		{name: "Canada for the slug's company", sets: sets, facts: f, posting: slug,
			q: xcBYesNo("Are you authorized to work lawfully in Canada for US Mobile Inc?"), entry: "work-authorization-other-countries", label: "No"},
	})
}

// Immuta (Lever): "an employment visa or other employer-sponsored immigration
// benefit"; SmarterDx (Greenhouse): a petition filed on the applicant's
// behalf, followed by what a citizen and a non-citizen should answer and the
// company's notice. A note that tells a citizen "yes" is not the need's.
func TestXC_AnOtherEmployerSponsoredBenefitAPetitionAndTheCitizenAnswerNotes(t *testing.T) {
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	const petition = "Will you now or in the future require SmarterDx to file a petition or application for employment-based visa status on your behalf to begin or continue employment with our company?"
	notes := ` If you are a US citizen, your answer to this question should be "no." If you are not a US citizen and you have non-immigrant status, for example F-1 or H-1, your answer to this question should be “yes.” Please note that we regretfully cannot provide sponsorship for new employment-based visa status at this time.`
	xcBRunCases(t, []xcBCase{
		{name: "other employer-sponsored benefit", sets: sets, facts: f, posting: match.Posting{Company: "immuta", Location: "Boston, MA"},
			q:     xcBQ("Will you now or in the future require Immuta to sponsor an employment visa or other employer-sponsored immigration benefit in order for you to begin or continue working for Immuta?", match.KindRadioGroup, "Yes", "No", "Unsure"),
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a petition with the citizen notes", sets: sets, facts: f, posting: match.Posting{Company: "smarterdx", Location: "Remote (United States)"},
			q: xcBYesNo(petition + notes), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a note that tells a citizen yes", sets: sets, facts: f, posting: match.Posting{Company: "smarterdx", Location: "Remote (United States)"},
			q: xcBYesNo(petition + ` If you are a US citizen, your answer to this question should be "yes."`), wantNone: true},
	})
}

// Climate First Bank (Greenhouse, live): "U.S" with no closing period is the
// United States.
func TestXC_TheUSWithoutItsClosingPeriod(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us-any-employer": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "U.S Employer", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you legally authorized to work for any U.S Employer?"), entry: "work-auth-us-any-employer", label: "Yes"},
	})
}

// Fay (Greenhouse, live): "How many years of experience do you have?" over
// "1-2" to "10+" is a multi-value listbox that asks for one range.
func TestXC_YearsInAMultiValueListbox(t *testing.T) {
	q := xcBQ("How many years of experience do you have?", match.KindCustomListbox, "1-2", "3-5", "6-9", "10+")
	q.InputType = match.InputMultiSelect
	xcBRunCases(t, []xcBCase{
		{name: "one range", sets: map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("1")}, facts: xcBFacts(), q: q,
			entry: "years-full-time-work-experience-number", label: "1-2"},
	})
}

// NMI (Greenhouse, live): "without being sponsored" is "without
// sponsorship", which a recorded need answers No.
func TestXC_AuthorizedWithoutBeingSponsored(t *testing.T) {
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes"), "work-auth-us": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "without being sponsored", sets: sets, facts: f, posting: match.Posting{Company: "NMI", Location: "Remote, US"},
			q: xcBYesNo("Are you authorized to work in the United States, without being sponsored?"), entry: "sponsorship-now-or-future", label: "No"},
	})
}

// Belvedere (Lever) and Aquatic (Greenhouse), live: the sponsorship asked "to
// maintain lawful employment", or by "the firm" "to obtain, maintain, or extend
// your employment authorization in the location(s) for which you are applying"
// under its sentence-case title, names no second requirement; a future need
// answers Yes and no need answers No.
func TestXC_SponsorshipForAPurposeTheFirmAndThePlaceAppliedTo(t *testing.T) {
	need := xcBFacts()
	need.RequiresSponsorshipNow, need.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	none := xcBFacts()
	none.RequiresSponsorshipNow, none.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	belvedere := xcBYesNo("Will you need sponsorship at any point in the future to maintain lawful employment in the United States?")
	belvedere.Required = true
	opts := []string{"Yes, will require firm sponsorship", "No. already has permanent work authorization"}
	aquatic := xcBQ("Employment eligibility status Will you require the firm's sponsorship to obtain, maintain, or extend your employment authorization in the location(s) for which you are applying?", match.KindCustomListbox, opts...)
	aquatic.InputType, aquatic.Required = match.InputYesNo, true
	xcBRunCases(t, []xcBCase{
		{name: "lawful employment, a future need", sets: sets, facts: need, posting: match.Posting{Company: "Belvedere Trading", Location: "Chicago, Illinois"}, q: belvedere, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "the firm, the place applied to", sets: sets, facts: need, posting: match.Posting{Company: "Aquatic Capital Management", Location: "Chicago; New York"}, q: aquatic, entry: "sponsorship-now-or-future", label: opts[0]},
		{name: "the firm, no need", sets: sets, facts: none, posting: match.Posting{Company: "Aquatic Capital Management", Location: "Chicago; New York"}, q: aquatic, entry: "sponsorship-now-or-future", label: opts[1]},
	})
}

// Valkyrie Trading (Lever, queue775): a certification that the answers on
// work authorization and sponsorship are true is ticked, as a certification
// that states the need is not; "What type of work environment are you
// seeking?" over one box per mode ticks the recorded modes; "Which institution
// granted (or will grant) this degree?" is the most recent school, an
// undergraduate one is not. The boxes carry the input types the engine sends
// (queue785): a lone box is yes_no, Lever's list of several is multi_select.
func TestXC_ValkyrieCertificationWorkEnvironmentAndInstitution(t *testing.T) {
	const certify = "I certify that all responses in this application—including my statements regarding current legal work authorization and future visa sponsorship requirements—are accurate and truthful. I acknowledge that misrepresenting any facts on this application is grounds for immediate rejection or termination of employment."
	box := func(text string, labels ...string) match.Question {
		q := xcBQ(text, match.KindCheckbox, labels...)
		q.InputType, q.Required = match.InputMultiSelect, true
		if len(labels) == 1 {
			q.InputType = match.InputYesNo
		}
		return q
	}
	modes := func(m ...string) match.Facts {
		f := xcBFacts()
		f.WorkModes = m
		return f
	}
	const env = "What type of work environment are you seeking?"
	school := map[string]xcBSet{"most-recent-school": xcBAns("Carver University")}
	schools := func(text string) match.Question {
		q := xcBQ(text, match.KindSelect, "Select a university or college", "Australian National University", "Carver University", "Other")
		q.InputType, q.Required = match.InputSingleSelect, true
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "the answers certified true", facts: xcBFacts(), legal: true, entry: "truth-certification", label: "I affirm and agree",
			q: box(certify, "I affirm and agree")},
		{name: "a certification that states the need", facts: xcBFacts(), legal: true, wantNone: true,
			q: box("I certify that all my answers, including those regarding sponsorship, are true, and that I will not require visa sponsorship.", "I affirm and agree")},
		{name: "every recorded mode", facts: modes("onsite", "hybrid", "remote"), q: box(env, "In-person", "Hybrid", "Fully remote"), answer: "In-person, Hybrid, Fully remote"},
		{name: "a box that names no mode", facts: modes("onsite", "hybrid", "remote"), q: box(env, "In-person", "Hybrid", "Four-day week"), wantNone: true},
		{name: "the degree's institution", sets: school, facts: xcBFacts(), q: schools("Which institution granted (or will grant) this degree?"), entry: "most-recent-school", label: "Carver University"},
		{name: "the undergraduate institution", sets: school, facts: xcBFacts(), q: schools("Which institution granted your undergraduate degree?"), notEntry: "most-recent-school", wantNone: true},
	})
}

// Windfall (Lever, queue778) asks SmarterDx's petition question with the
// note "If you have non-immigrant status, ..., your answer to this question
// should be "yes."", the citizen note without the citizen; Keeper Security
// (Greenhouse) asks the company "to sponsor, file for or support an
// immigration-related employment benefit so that you may begin or continue
// working for us". A note telling a non-immigrant "no", and a purpose that is
// not the work, are not read.
func TestXC_ANonImmigrantAnswerNoteAndAListOfSponsorVerbs(t *testing.T) {
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	windfall := match.Posting{Company: "Windfall", Location: "San Francisco"}
	keeper := match.Posting{Company: "Keeper Security", Location: "Remote, US"}
	const petition = "Will you now or in the future require Windfall to file a petition or application for employment-based visa status on your behalf to begin or continue employment with our company?"
	const verbs = "Will you now or at any time in the future require our company to sponsor, file for or support an immigration-related employment benefit so that you may "
	const examples = "? Examples may include H-1B, O-1, E-3, TN, permanent-residence sponsorship or support connected with an EB-2 National Interest Waiver."
	xcBRunCases(t, []xcBCase{
		{name: "the non-immigrant note", sets: sets, facts: f, posting: windfall,
			q: xcBYesNo(petition + " If you have non-immigrant status, for example F-1 or H-1, your answer to this question should be “yes.”"), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a note that tells a non-immigrant no", sets: sets, facts: f, posting: windfall,
			q: xcBYesNo(petition + ` If you have non-immigrant status, for example F-1 or H-1, your answer to this question should be "no."`), wantNone: true},
		{name: "sponsor, file for or support", sets: sets, facts: f, posting: keeper,
			q: xcBYesNo(verbs + "begin or continue working for us" + examples), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a purpose that is not the work", sets: sets, facts: f, posting: keeper,
			q: xcBYesNo(verbs + "relocate" + examples), wantNone: true},
	})
}

// Windfall (Lever, queue785): "This position is a hybrid role in San Francisco
// or Denver. We are currently not considering relocation for candidates. Where
// are you located?" in a text box. Statements that address no one are the
// role's and the company's; the box is the applicant's location.
func TestXC_ACompanyPolicyOnRelocationBeforeTheLocationAsk(t *testing.T) {
	f := xcBFacts()
	f.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	windfall := match.Posting{Company: "Windfall", Location: "San Francisco, CA; Denver, CO"}
	xcBRunCases(t, []xcBCase{
		{name: "the location after the policy", facts: f, posting: windfall,
			q: xcBText("This position is a hybrid role in San Francisco or Denver. We are currently not considering relocation for candidates. Where are you located?"), answer: "Charleston, SC"},
		{name: "a lead that tells the applicant the arrangement", facts: f, posting: windfall,
			q: xcBText("You would work hybrid from our Denver office. Where are you located?"), wantNone: true},
	})
}

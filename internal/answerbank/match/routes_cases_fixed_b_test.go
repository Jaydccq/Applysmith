package match

// routeCasesFixedB merges the table cases of the second fixed-entry family.
func routeCasesFixedB() map[int]ruleCases {
	return mergeRuleCases(
		rxbCasesDisclosure(),
		rxbCasesContact(),
		rxbCasesAuthorization(),
		rxbCasesLocation(),
		rxbCasesEmployment(),
		rxbCasesEducation(),
	)
}

func rxbCasesDisclosure() map[int]ruleCases {
	const criminalSentence = "Have you been debarred by the FDA? This question is not requesting any information relating to criminal arrests or criminal convictions."
	return map[int]ruleCases{
		454: {
			Pos: []string{"Is anyone in your household involved in federal agency procurement?", "Do you have a role in federal procurement decisions?"},
			Neg: []string{"Do you have experience with procurement software?", "Are you registered with a state purchasing portal?",
				"Are you on the list of parties excluded from federal procurement?"},
		},
		455: {
			Pos: []string{"Is there a pending contract with a federal agency involving procurement at Example Corp?"},
			Neg: []string{"Is there a pending contract with Example Corp?", "Have you ever worked on a federal grant proposal?"},
		},
		456: {
			Pos: []string{"Have you been involved in the award or administration of a contract with the U.S. government?", "Did you administer any contract for the Department of Defense?"},
			Neg: []string{"Did you administer the Example Corp onboarding program?", "Have you signed a lease contract?"},
		},
		457: {
			Pos: []string{"Do you have any matters that may affect the financial interests of Example Corp?"},
			Neg: []string{"Do you have any matters pending in small claims court?", "Which matters are most important to you at work?"},
		},
		458: {
			Pos: []string{"Could this role affect your financial interest?", "Might your outside work impact the financial interests of the company?"},
			Neg: []string{"Is your financial planning experience current?", "Will the schedule affect your commute?"},
		},
		459: {
			Pos: []string{"Have you ever been investigated or debarred by the FDA?", "Has the Department of Health and Human Services restricted your participation in clinical work?", "Have you been disqualified from clinical trials by the Food and Drug Administration?"},
			Neg: []string{criminalSentence, "Have you ever been investigated by a licensing board?"},
		},
		460: {
			Pos: []string{"Have you worked on an investigational drug?", "Have you worked with a new drug application?"},
			Neg: []string{"Have you worked on an investigational drug? Please do not list any criminal convictions.", "Do you have experience with generic software tools?"},
		},
		461: {
			Pos: []string{"Are you subject to the Generic Drug Enforcement Act?"},
			Neg: []string{"Are you subject to the Example Privacy Act?", "Are you subject to the Generic Drug Enforcement Act? Exclude any criminal arrests."},
		},
		462: {
			Pos: []string{"Have you been debarred under 21 U.S.C. 335a?", "Are you subject to debarment relating to generic drug applications?"},
			Neg: []string{"Have you been debarred from entering Springfield?", "Are you debarred under 21 U.S.C. 335a? Do not include criminal convictions."},
		},
		463: {
			Pos: []string{"Are you party to any debarment proceedings?"},
			Neg: []string{"Are you party to any billing proceedings?", "Are you party to any debarment proceedings? Ignore criminal records."},
		},
		464: {
			Pos: []string{"Is debarment pending against you?", "Is any debarment action threatened against you?"},
			Neg: []string{"Is a promotion pending for you?", "Is debarment pending against you? Skip criminal convictions."},
		},
		465: {
			Pos: []string{"Have you been excluded from participation in Medicare or Medicaid?", "Are you suspended from any federal health care program?"},
			Neg: []string{"Have you been excluded from the Example Corp newsletter?", "Have you been excluded from Medicare? Criminal arrests need not be disclosed."},
		},
		466: {
			Pos: []string{"Are you on the List of Excluded Individuals and/or Entities?", "Are you listed on the LEIE?", "Are you on an OIG exclusion list?"},
			Neg: []string{"Are you on the invitation list?", "Are you on the LEIE? Criminal convictions are not requested."},
		},
		467: {
			Pos: []string{"Are you listed by the General Services Administration?", "Are you on a GSA exclusion list?"},
			Neg: []string{"Are you listed in the staff directory?", "Are you on a GSA list? This is not about criminal arrests."},
		},
		468: {
			Pos: []string{"Are you on the list of parties excluded from federal procurement?", "Are you on the Excluded Parties List?", "Are you listed in EPLS?"},
			Neg: []string{"Are you on the waiting list?", "Are you on the excluded parties list? No criminal convictions please."},
		},
		469: {
			Pos: []string{"Have you ever been sanctioned for health care fraud?", "Have you been investigated for healthcare abuse?", "Are you subject to health care fraud and abuse penalties?"},
			Neg: []string{"Have you been sanctioned for tax filing errors?", "Have you ever been charged with health care fraud? List criminal convictions elsewhere."},
		},
		470: {
			Pos: []string{"Are you the subject of any pending inquiry by a governmental agency?", "Is there a proceeding before a licensing board concerning you?"},
			Neg: []string{"Is there an inquiry from the recruiting team?", criminalSentence + " Is there an investigation by a governmental agency?"},
		},
		471: {
			Pos: []string{"Has any administrative action been imposed upon you?"},
			Neg: []string{"Has any administrative task been assigned to you?", "Has any administrative action been imposed upon you? Not about criminal records."},
		},
		472: {
			Pos: []string{"Are you the subject of any pending inquiry?"},
			Neg: []string{"Are you the subject of this survey?", "Are you the subject of any pending inquiry? Not about criminal convictions."},
		},
		473: {
			Pos: []string{"Are you a licensed physician?", "Are you a physician licensed in Exampleland?", "Are you a U.S. medical doctor licensed to practice?"},
			Neg: []string{"Are you a licensed electrician?", "Are you a physician assistant?"},
		},
		474: {
			Pos: []string{"Have you ever been disqualified in writing?"},
			Neg: []string{"Have you ever been disqualified from a race?"},
		},
		475: {
			Pos: []string{"Have you been disqualified from any program, in writing, by a regulator?"},
			Neg: []string{"Have you been disqualified from a program?", "Please confirm in writing that you agree."},
		},
		479: {
			Pos: []string{"Do you consent to text messages from Example Corp about your application?", "Do you give permission to send you text messages?",
				"Do you opt-in to receive WhatsApp messages from Stripe Recruiting?",
				// Gopuff (Lever).
				"Do you consent to texts from the recruiting team at Gopuff regarding nexts steps and the status of the role you are applying to, along with future positions?"},
			Neg: []string{"Do you consent to a background check?", "Can we send you an email?", "Do you consent to the use of these texts in our research?"},
		},
		480: {
			Pos: []string{"I consent to receiving SMS updates about my candidacy.", "Do you consent to receive text messages?"},
			Neg: []string{"I consent to receiving emails about my candidacy."},
		},
		481: {
			Pos: []string{"Text messages may be sent during the hiring process.", "SMS notifications for recruiting are optional."},
			Neg: []string{"Text of the job description for the hiring process.", "You may reply to our email."},
		},
		482: {
			Pos: []string{"May we text you about this role?"},
			Neg: []string{"May we email you about this role?"},
		},
		483: {
			Pos: []string{"I authorize Example Corp to communicate with me via text.", "Do you agree that we may contact you via text?", "Do you agree to be contacted via WhatsApp?",
				"Would you like to receive communications via SMS and/or WhatsApp to the number provided on your application?"},
			Neg: []string{"I authorize Example Corp to communicate with me via email.", "Would you like to receive communications via email?"},
		},
		484: {
			Pos: []string{"Example Corp may communicate with me via text."},
			Neg: []string{"Example Corp may communicate with me via phone call."},
		},
		485: {
			Pos: []string{"Are you a tobacco user?", "Do you use tobacco?", "Do you smoke cigarettes?", "Do you use vaping products?"},
			Neg: []string{"Do you drink coffee?", "Are you a user of Example Framework?"},
		},
		486: {
			Pos: []string{"Are you a nurse practitioner licensed in this state?", "Are you a dentist who can practice here?"},
			Neg: []string{"Are you a licensed driver?", "Do you know a dentist."},
		},
		518: {
			Pos: []string{"Is our dog-friendly office a fit for you?", "Are you comfortable with dogs being present in the office?", "Dogs are present in the office environment. Are you okay with that?"},
			Neg: []string{"Do you have experience training machine learning models?", "Do you own a dog?"},
		},
		537: {
			Pos: []string{"What is your hourly pay rate?", "What hourly wage do you expect?"},
			Neg: []string{"What is your annual pay?", "How many hours per week can you work?"},
		},
		538: {
			Pos: []string{"What is your expected rate per hour?", "Desired compensation per hour"},
			Neg: []string{"What is your expected rate per project?", "What is your expected annual compensation?"},
		},
		539: {
			Pos: []string{"Desired hourly", "Preferred hourly range"},
			Neg: []string{"Desired annual salary", "Hourly roles only"},
		},
		540: {
			Pos: []string{"Are you open to W2 only positions?", "W-2 only role, correct?",
				"This role is a W-2 position employed through a payroll agency, rather than a 1099 independent contractor arrangement. Are you comfortable with this employment arrangement?",
				"This is a W2 contract. Are you okay with that?"},
			Neg: []string{"Are you open to W2 or 1099 roles?", "Are you open to contract roles?", "W-2 or C2C? Are you comfortable with that?",
				"W2 or 1099 roles? Are you open to either?"},
		},
		541: {
			Pos: []string{"This role is W2 exclusively. Is that acceptable?", "W2 position, no C2C or 1099 allowed. Acceptable?", "Are you available for W-2 work only?"},
			Neg: []string{"Is a corp-to-corp arrangement acceptable?", "Do you have a W2 from last year?"},
		},
		542: {
			Pos: []string{"Are you open to C2C engagements?", "Do you prefer c-2-c contracts?"},
			Neg: []string{"W2 only, no 1099 or C2C. Is this acceptable?", "Are you open to W2 engagements?"},
		},
		543: {
			Pos: []string{"Is corp to corp acceptable?", "Are you available on a corp-to-corp basis?"},
			Neg: []string{"W2 only, no corp-to-corp. Acceptable?", "Is a staffing agency acceptable?"},
		},
	}
}

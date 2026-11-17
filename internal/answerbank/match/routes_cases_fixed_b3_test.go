package match

func rxbCasesAuthorization() map[int]ruleCases {
	return map[int]ruleCases{
		12: {
			Pos: []string{"Have you become a permanent resident of another country?", "Since obtaining your citizenship, have you been a permanent resident elsewhere?"},
			Neg: []string{"Are you a permanent resident of Exampleland?", "Do you hold citizenship?"},
		},
		13: {
			Pos: []string{"Do you currently hold citizenship in any other country?", "Are you a citizen of another country?"},
			Neg: []string{"Since obtaining citizenship, do you hold citizenship in another country?", "Do you hold citizenship in Exampleland?"},
		},
		19: {
			Pos: []string{"When does your work authorization expire?", "Employment authorization expiration date"},
			Neg: []string{"What is your work authorization status?"},
		},
		20: {
			Pos: []string{"Please provide the amount of remaining work authorization.", "What type of visa do you hold and how much time is remaining?",
				"What is your eligibility to work permanently in the US?"},
			Neg: []string{"Please provide your work authorization.", "What type of visa is this?"},
		},
		60: {
			Pos: []string{"Can you work without sponsorship?", "Are you able to work without requiring visa sponsorship?", "No visa sponsorship is needed",
				"Are you legally authorized to work in the U.S. without current or future sponsorship?",
				// Eventual (Ashby, live).
				"Are you authorized to work lawfully in the US, without employer support?",
				// SecurityScorecard (Greenhouse, live).
				"Are you currently authorized to work in the country in which you reside, without requiring current or future visa/work permit sponsorship from the company?",
				"Can you work without visa or work permit sponsorship?"},
			Neg: []string{"Do you require sponsorship?", "Will you need a visa?", "Can you work without support from your manager?", "Are you permanently authorized to work in the United States without visa sponsorship? Select \"Yes\" if you are a U.S. citizen, Green Card holder, or have a work permit that does not require company sponsorship."},
		},
		61: {
			Pos: []string{"Do you have permanent authorization to work in the US?", "Are you permanently authorized to work in the country?", "Are you eligible to work permanently in the United States?",
				// Dave (Ashby, live).
				"Are you permanently authorized to work in the United States without visa sponsorship? Select \"Yes\" if you are a U.S. citizen, Green Card holder, or have a work permit that does not require company sponsorship."},
			Neg: []string{"Do you have temporary authorization to work in the US?", "Are you eligible to work in the US?"},
		},
		64: {
			Pos: []string{"Are you legally authorized to work in the United States?", "Are you authorized to work lawfully in the U.S.?", "Are you authorized to be employed in the United States?",
				// Agentio (Ashby, live): the job's hours between the work and the place.
				"Are you authorized to work full time in the United States?", "Are you legally authorized to work full-time in the US?"},
			Neg: []string{"Are you authorized to work in the United States for any employer?", "Are you authorized to work in the US without requiring sponsorship?", "Are you authorized to work in Canada?",
				"If you are authorized to work in the United States, is that authorization pursuant to an Employment Authorization Document (EAD)? If yes, please indicate the applicable EAD category code."},
		},
		65: {
			Pos: []string{"Is your work permit valid?", "Do you hold a visa for the country you are applying to?"},
			Neg: []string{"Will you require a visa that is already valid?", "Is your parking permit valid?"},
		},
		67: {
			Pos: []string{"Do you have authorization to work in the United States?"},
			Neg: []string{"Do you require visa authorization to work in the US?", "Do you have authorization to drive in the United States?",
				"Currently, or in the future, does your authorization to work in the United States involve Curricular Practical Training (CPT) or Optional Practical Training (OPT)?",
				"Do you have unlimited and unrestricted authorization to work in the United States?"},
		},
		69: {
			Pos: []string{"Can you provide proof that you are authorized to work?", "Are you authorized to work within three days of hire?"},
			Neg: []string{"Can you provide proof of your identity?", "Are you authorized to work within three days of hire if you need a visa?"},
		},
		70: {
			Pos: []string{"Are you eligible to work in the U.S.?", "Are you currently eligible to work legally in the United States of America?", "Are you eligible and legally able to work in the United States?",
				// Automox (Greenhouse, live).
				"Are you legally able to work in the US?", "Are you able to legally work in the United States?"},
			Neg: []string{"Are you eligible to work in Exampleland?", "Are you eligible to work in the US if you need sponsorship?", "Are you legally able to work in Exampleland?"},
		},
		71: {
			Pos: []string{"Are you eligible to work in Canada?", "Are you authorized to work in Canada?", "Are you eligible to work in Mexico?",
				// MotherDuck (Ashby, Amsterdam).
				"Are you currently eligible to work in the Netherlands?", "Are you authorized to work in Germany?", "Are you entitled to work in the UK?"},
			Neg: []string{"Are you eligible to work in Exampleland?", "Are you eligible to work in Canada if you require a visa?", "Are you eligible to work in this role on a hybrid schedule?",
				// The United States is its own routes'.
				"Are you legally authorized to work in the United States for ANY employer without ANY restrictions?"},
		},
		72: {
			Pos: []string{"Do you have the right to work in the UK?", "Do you have the right to work in Britain?"},
			Neg: []string{"Do you have the right to work in France?"},
		},
		73: {
			Pos: []string{"Are you legally permitted to work in the country where this job is located?"},
			Neg: []string{"Are you permitted to work in the country where this job is located?"},
		},
		74: {
			Pos: []string{"Are you legally authorized to work in the country in which you are applying?",
				"Are you authorized to work in the country for which you applied?", "Are you authorized to work in the country you have applied to?",
				"Are you eligible to work in the country you are applying?", "Are you legally entitled to work in the country you are applying for?",
				// Wayve (Ashby, live): "you're".
				"Are you eligible to work in the country where you're applying?", "Are you authorized to work in the country for which role you are applying?",
				// Neo4j (Greenhouse, live): "able to legally work", in "the region".
				"Are you able to legally work in the region you are applying for?", "Are you legally able to work in the country you are applying to?"},
			Neg: []string{"Are you authorized to work in the country in which you were born?", "Are you authorized to work in the country you applied from?",
				// Able, with no law named, is not authorization.
				"Are you able to work in the country you are applying for?"},
		},
		577: {
			Pos: []string{"Available from", "Availability date", "Available starting on:"},
			Neg: []string{"Available from which office?", "Are you available from Monday to Friday?"},
		},
		576: {
			Pos: []string{"Do you currently have legal US work authorization?", "Do you hold valid U.S. work authorization?"},
			Neg: []string{"What is your US work authorization status?", "Do you currently have legal US work authorization, or will you require visa sponsorship?"},
		},
		75: {
			Pos: []string{"Confirm your eligibility to work in the country in which you are applying.",
				// HackerOne (Ashby, live).
				"Can you provide proof of authorization to work in the country for which job you are applying for?"},
			Neg: []string{"Confirm your eligibility to apply in the country in which you are applying.",
				"Will you require authorization to work in the country you are applying to?"},
		},
		76: {
			Pos: []string{"Are you legally authorized to work at the posted locations?", "Are you authorized to work in the location you selected?", "Are you legally eligible to work in the job's location?", "Are you legally authorized to work in the job posting country?",
				// Block (Greenhouse, live): the adverb after the verb, the participle after the noun.
				"Are you authorized to work lawfully in the location posted for this position?",
				// Navan (Greenhouse, queue155): the country the posting outlines.
				"Are you currently eligible to work in the country outlined for this position?",
				// Artificial Analysis (Ashby, queue167): the office chosen just above.
				"Work Authorization: Are you currently legally authorized to work in the country of the office you selected? San Francisco = United States. Melbourne = Australia.",
				// 4M (Ashby): the preferred location for this posting.
				"Are you legally authorized to work in your preferred location?",
				// Aptos Labs (Greenhouse, queue762): the location applied for.
				"Are you authorized to work in the location you are applying for?", "Are you eligible to work in the location you're applying to?"},
			Neg: []string{"Are you authorized to work in any of the posted locations for any employer?", "Which location did you select?", "Which office did you select?",
				"Which location are you applying for?",
				"Which country is outlined for this position?",
				"Are you authorized to work lawfully in the location you live in?"},
		},
		77: {
			Pos: []string{"Are you legally authorised to work full-time in the country where this job is based?",
				"Are you legally authorized to work in the country in which the job you're applying for is located?", "Are you legally authorized to work in the country where this job is posted?", "Do you currently have legal work authorization in the country where this position is based?",
				"Are you legally authorized to work in the country or jurisdiction where the position to which you are applying is located?",
				"Do you have work authorization to work in the country where this job is located?",
				"Do you have the work authorization to legally work in the country this role is based?",
				// Dust (Ashby, live): the role's country stands behind "for this role".
				"Do you have the appropriate work authorization for this role? Local Citizen/Visa",
				"Are you currently authorized to work in the city/country where this position is located?",
				// Lime (Ashby, live).
				"Are you currently authorized to work in the country in which this job is posted?",
				// EarnIn (Greenhouse, live): "that" for "in which", the "in" left out.
				"Are you legally authorized to work in the country that this role is based?",
				// SoFi (Greenhouse, live): the adverb inside the infinitive.
				"Are you authorized to lawfully work in the country where this role is located?",
				// Cribl (Greenhouse, live): residing beside working.
				"Are you currently authorized to reside and work in the country where this role is based?",
				// Perk (Ashby, live): the noun, held.
				"Do you have legal authorisation to work in the country where this role is based?"},
			Neg: []string{"Are you authorized to work in the country where this role is posted for any employer?", "Are you authorized to work in the country where you were born?",
				"Do you have work authorization to work in that country?"},
		},
		78: {
			Pos: []string{"Are you eligible to work in the country where the position is located?",
				// Mindbody (Greenhouse, live): the role's office.
				"Are you currently eligible to work in the country where the job office is located?"},
			Neg: []string{"Are you eligible to work in the country where you currently live?", "Are you eligible to work in the country where the head office is located?"},
		},
		79: {
			Pos: []string{"Do you have the legal right to work in the US?"},
			Neg: []string{"Do you have the right to work in the US without needing sponsorship?", "Do you have the right to work in Exampleland?",
				// Elastic: the unrestricted right is route 91's.
				"Do you have the unrestricted right to work in the United States?"},
		},
		80: {
			Pos: []string{"Do you have the legal right to work in this country?"},
			Neg: []string{"Do you have the right to work in Exampleland?", "Do you have the right to work in this country if you need a visa?"},
		},
		81: {
			Pos: []string{"Are you authorized to work lawfully in this country?", "Are you eligible to work in that country?",
				// Clera (Ashby) and Alnylam (Greenhouse), live.
				"Do you have work authorization to work in that country?",
				"Are you legally authorized to work in the country/region of the job for which you are applying?"},
			Neg: []string{"Are you authorized to work in this country for any employer?", "Are you authorized to work in a country?",
				"Will you require employer support to obtain or maintain authorization to work in that country?"},
		},
		82: {
			Pos: []string{"Can you present documents proving your right to work?", "Can you, upon employment, submit verification of your legal right to work in the countries to which you have applied?"},
			Neg: []string{"Can you present your portfolio?", "Can you present documents proving your right to work if you require a visa?", "Can you submit verification of your degree?"},
		},
		83: {
			Pos: []string{"Do you have the right to legally work in the country where this job is located?"},
			Neg: []string{"Do you have the right to work in the country where this job is located?"},
		},
		84: {
			Pos: []string{"Can you show proof of your right to work?", "If hired, can you provide proof that you are legally able to work in the United States?", "Do you have proof of your right to work?"},
			Neg: []string{"Can you show proof of your degree?", "If hired, can you provide proof of your degree from the United States?"},
		},
		85: {
			Pos: []string{"Can you provide documentation of your legal right to work in the US?", "Do you have documentation of your legal right to work lawfully within the U.S.?"},
			Neg: []string{"Can you provide documentation of your degree in the US?"},
		},
		86: {
			Pos: []string{"Do you have documents for the Form I-9 showing your right to work?"},
			Neg: []string{"Please complete the Form I-9 survey."},
		},
		87: {
			Pos: []string{"Do you currently have authorization to work lawfully?"},
			Neg: []string{"Do you currently have authorization to work for any employer?", "Do you currently have authorization to work if you need sponsorship?"},
		},
		88: {
			Pos: []string{"Are you authorized to work in the United States for any employer?",
				// Pika (Ashby).
				"Do you have authorization to work for any employer in the US?"},
			Neg: []string{"Are you authorized to work in the United States?", "Do you require sponsorship to be authorized to work in the U.S. for any employer?",
				// Rain (Ashby, live): without restrictions is the unrestricted entry's (route 92).
				"Are you legally authorized to work in the United States for ANY employer without ANY restrictions?"},
		},
		89: {
			Pos: []string{"Do you have valid legal authorization to work for any employer in the country where this job is posted?"},
			Neg: []string{"Do you have authorization to work in the country where this job is posted?"},
		},
		90: {
			Pos: []string{"Are you authorized to work for any employer in the country where this job is located?"},
			Neg: []string{"Are you authorized to work for any employer?"},
		},
		93: {
			Pos: []string{"Are you a U.S. person under ITAR?", "Do export control rules make you a U.S. person?",
				// Mattermost (Greenhouse, live).
				"Are you eligible to access export-controlled information as required by U.S. export control laws (EAR/ITAR)?"},
			Neg: []string{"Are you a U.S. person?", "Are you a U.S. person with early availability?", "Have you worked with export-controlled information before?"},
		},
		100: {
			Pos: []string{"Are you on a student visa such as OPT or CPT?", "Is your work authorization based on OPT?"},
			Neg: []string{"Do you hold a temporary visa?", "Are you on a student schedule?"},
		},
		336: {
			Pos: []string{"Are you a U.S. person?", "Are you a US person?",
				// Okta (live): the note on who is not a U.S. person names sponsorship.
				"Are you a U.S. Person (U.S. Citizen, National, Lawful Permanent Resident, Refugee, or Asylee), and can you provide documentation establishing U.S. Person status upon hire? Please note: Candidates who require visa sponsorship (e.g. H-1B, F-1, O-1, L-1, etc.) are not U.S. Persons."},
			Neg: []string{"Are you a person who likes Example Corp?"},
		},
		337: {
			Pos: []string{"Are you a U.S. citizen or lawful permanent resident?", "Are you a citizen of the United States, a lawful permanent resident, an asylee or a refugee?"},
			Neg: []string{"Are you a citizen of Exampleland?", "Are you a citizen of the U.S.?", "Are you a lawful permanent resident?", "Are you an asylee?", "Are you a refugee?"},
		},
		338: {
			Pos: []string{"Are you a citizen of Iran?", "Are you a citizen of North Korea?"},
			Neg: []string{"Are you a citizen of Springfield?"},
		},
		339: {
			Pos: []string{"Are you a national of any of the following: 1) Cuba, 2) Syria?",
				"Are you a foreign person of an “Embargoed Country,” as denoted by the U.S. Government, that may require a license for deemed export purposes?"},
			Neg: []string{"Are you a national of the United States?"},
		},
		105: {
			Pos: []string{"Describe the type of sponsorship you may require.", "What kind of sponsorship will you need?",
				"If you do require employee sponsorship or assistance for work authorization, please list the type of support you may require.",
				"What type of immigration support would you require, if hired?",
				// Casca (Ashby, live): the ask to specify after the question.
				"Do you have any current or future sponsorship needs? Please specify, such as H1B transfer/new H1B/TN/etc.",
				// Stand Insurance (Ashby, live): the details asked after the authorization.
				"Are you authorized to work in the US? If you need sponsorship, please provide details."},
			Neg: []string{"Do you require sponsorship?", "Will you need a visa?", "Will you require immigration support?", "Do you have any sponsorship needs?",
				"If you need sponsorship, you are not eligible for this role."},
		},
		106: {
			Pos: []string{"Will you now or in the future require sponsorship?", "Do you require employer-sponsored work authorization?", "Will you now or will you in the future require employment visa sponsporship?"},
			Neg: []string{"Do you require sponsorship for this hybrid role?", "Do you want a sponsor?",
				"Are you a U.S. person? Candidates who require visa sponsorship are not U.S. persons."},
		},
		107: {
			Pos: []string{"Is sponsorship needed now or in the future?"},
			Neg: []string{"Will sponsorship matter in the future? This role is in-person.", "Sponsorship is not offered."},
		},
		108: {
			Pos: []string{"Visa status", "Visa sponsorship"},
			Neg: []string{"Visa sponsorship for this on-site role", "Visa types",
				"Are you a U.S. person? *Temporary residence does not mean visa status such as B, H, E, L, F, or J."},
		},
		110: {
			Pos: []string{"Will you require work authorization of any kind?", "Do you need a work permit to work here?", "Do you require any form of authorization to work legally in the United States?"},
			Neg: []string{"Do you have a work permit?", "Do you need a visa to work from the in-person office?", "Do you have any form of authorization to work in the United States?"},
		},
		111: {
			Pos: []string{"Do you need assistance with your work authorization?"},
			Neg: []string{"Do you need assistance with your timesheet?", "Do you need assistance with your work authorization at the on-site office?"},
		},
		112: {
			Pos: []string{"Will you require immigration support?", "Do you need immigration support to maintain work authorization?"},
			Neg: []string{"Please describe whether you will need immigration support.", "Tell me about immigration law.", "If you do/would require immigration support, would you need an H1B, O1 visa, or transfer of either?"},
		},
		113: {
			Pos: []string{"Do you require employer support to maintain your work authorization?"},
			Neg: []string{"Please explain if you require company support to obtain a visa.", "Do you offer employer support for events?"},
		},
		114: {
			Pos: []string{"Will you need to file an H-1B transfer?", "H-1B cap registration status", "Would you need an H-1B transfer?"},
			Neg: []string{"What is your non-immigrant status?", "If you do/would require immigration support, would you need an H1B, O1 visa, or transfer of either?"},
		},
		115: {
			Pos: []string{"Will you be seeking H-1B sponsorship?", "If you do/would require immigration support, would you need an H1B, O1 visa, or transfer of either?"},
			Neg: []string{"Do you hold an H-1B?", "Do you hold an h1b visa?", "Are you currently on an H-1B visa?", "Is H-1B sponsorship offered?"},
		},
		117: {
			Pos: []string{"Are you based in the US with a valid work permit?"},
			Neg: []string{"Are you based in the US?", "Are you based in Exampleland with a work permit?"},
		},
	}
}

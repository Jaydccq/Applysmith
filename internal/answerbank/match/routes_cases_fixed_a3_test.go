package match

func routeCasesFixedAWotc() map[int]ruleCases {
	return map[int]ruleCases{
		3: {
			Pos: []string{"Have you been unemployed for at least 27 consecutive weeks?"},
			Neg: []string{"Are you currently unemployed?", "Have you worked for 27 consecutive weeks?"},
		},
		4: {
			Pos: []string{"Are you a recipient of Supplemental Nutrition Assistance?", "Have you received SNAP benefits?", "SNAP received for at least 3 months"},
			Neg: []string{"Do you want to snap a photo?", "Do you receive an allowance?", "Have you interviewed with Snap in the past 6 months?"},
		},
		5: {
			Pos: []string{"Were you referred by a vocational rehabilitation agency?", "Vocational rehab referral"},
			Neg: []string{"Do you have vocational training?"},
		},
		6: {
			Pos: []string{"Is a member of your family a TANF recipient?", "Temporary Assistance for Needy Families"},
			Neg: []string{"Do you receive family leave?"},
		},
		7: {
			Pos: []string{"Were you convicted of a felony during the year before you were hired?", "Were you released from prison for a felony?"},
			Neg: []string{"Have you been convicted of a felony?", "Were you released from prison?"},
		},
		8: {
			Pos: []string{"Do you receive Supplemental Security Income?", "Are you receiving SSI benefits?"},
			Neg: []string{"Do you receive social security?", "What is your SSN?"},
		},
		9: {
			Pos: []string{"Have you ever been convicted of a crime?", "Do you have a criminal record?", "Have you pleaded guilty to any offence?", "Do you have a criminal history?", "Do you have any criminal convictions?"},
			Neg: []string{
				"Were you convicted of a felony in the year before you were hired?",
				"Have you been convicted of a firearms offence?",
				"Are you under indictment or have a conviction for export control violations?",
				"Do you like criminal justice podcasts?",
			},
		},
		10: {
			Pos: []string{"Have you held H-1B status in the preceding 6 years?", "Have you held H-1B status for six years?"},
			Neg: []string{"Do you require H-1B sponsorship?", "Have you held a driver license for 6 years?"},
		},
		11: {
			Pos: []string{"Have you ever held J-1 status?", "Held J1 status in the past"},
			Neg: []string{"Do you hold an F-1 visa?", "Have you ever held a job?"},
		},
		15: {
			Pos: []string{"Have you lived outside the US for 12 consecutive months?", "Are you located outside the United States for twelve consecutive months?"},
			Neg: []string{"Are you located in the United States?", "Have you lived outside the city?"},
		},
		17: {
			Pos: []string{"Are you 18+?", "Are you at least 21 years old?", "Are you 18 years of age or older?", "Must be 18+ years of age."},
			Neg: []string{"Are you at least 18? Do you have a work permit?", "Are you authorized to work?", "What is your age?",
				"Are you a U.S. person? A temporary resident is an alien that has performed seasonal agricultural services for at least 90 days during the 12-month period ending on May 1, 1986."},
		},
		21: {
			Pos: []string{"When does your STEM OPT expire?", "STEM OPT end date"},
			Neg: []string{"Are you eligible for a STEM OPT extension?", "When does your lease expire?"},
		},
		29: {
			Pos: []string{"Birthday", "What is your birthday?"},
			Neg: []string{"Is it a holiday?"},
		},
		42: {
			Pos: []string{"Please list any relatives employed by Example Corp.", "Name of any relative who works here", "Names of relatives"},
			Neg: []string{"Do you have relatives employed by Example Corp?", "List your skills"},
		},
		51: {
			Pos: []string{"Non-compete agreement: please explain the details.", "If yes, please explain the non-compete."},
			Neg: []string{"Are you subject to a non-compete?", "Please explain your gap in employment."},
		},
		52: {
			Pos: []string{"Are you subject to a non-compete?", "Do you have a non-solicitation agreement?", "Noncompete"},
			Neg: []string{"Are you subject to a restrictive covenant?", "Do you have a driver license?"},
		},
		53: {
			Pos: []string{"Do you have any post-employment restrictions?", "Post employment covenants with a former employer"},
			Neg: []string{"What is your employment history?", "Do you have post-graduate education?"},
		},
		54: {
			Pos: []string{"Are you bound by a restrictive covenant?", "Are you on garden leave?"},
			Neg: []string{"Are you bound by an NDA?", "Do you have a leave of absence?"},
		},
		55: {
			Pos: []string{"Are there contractual restrictions on your ability to work for us?", "Contractual restrictions limiting working at Example Corp"},
			Neg: []string{"Are there contractual obligations to your prior employer?", "Your ability to work remotely"},
		},
		91: {
			Pos: []string{"Do you have the unrestricted right to work in the United States?", "Unrestricted right to work in the U.S.", "Unrestricted right to work in the US?"},
			Neg: []string{"Do you have the unrestricted right to work in the US and require sponsorship?", "Do you have the right to work in the United States?"},
		},
		92: {
			Pos: []string{"Do you have unrestricted work authorization?", "Unrestricted work authorisation", "Do you have unlimited and unrestricted authorization to work in the United States?",
				"Are you legally authorized to work in the United States for ANY employer without ANY restrictions?"},
			Neg: []string{"Do you have unrestricted work authorization or need a visa?", "Do you have restricted work authorization?"},
		},
		101: {
			Pos: []string{"Are you currently in a period of OPT?", "Are you presently on optional practical training?", "Are you in a period of optional practical training?"},
			Neg: []string{"Are you eligible for CPT?", "Do you want to opt in to alerts?",
				"Will you now or in the future require us to sponsor any immigration paperwork, including OPT to H-1B conversion?"},
		},
		102: {
			Pos: []string{"Are you eligible for a 24-month STEM OPT extension?", "Do you need an OPT extension for STEM?", "Are you eligible for the OPT extension?"},
			Neg: []string{"What is your visa type?", "Do you need an extension on the deadline?"},
		},
		118: {
			Pos: []string{"Do you currently live in the United States?", "Are you currently located in the US or Canada?", "Are you based in the U.S.?"},
			Neg: []string{"Do you currently live in the Springfield area?", "Do you live in the use case?"},
		},
		119: {
			Pos: []string{"Where are you currently based?", "Where are you presently located?"},
			Neg: []string{"Are you currently based in Springfield?", "Where are you applying from?"},
		},
		124: {
			Pos: []string{"Are you currently based in Springfield?", "Do you currently live in Shelbyville?", "Are you local to Springfield, CA?",
				"This role is located in Santa Clara, CA or Bellevue, WA. Are you currently located within a drivable distance of either of those areas?",
				"Are you currently based out of the New York City area?",
				"This role is a hybrid role where you will be expected to work from our NY or SF office a few days a week. Are you located in the New York or SF area?",
				// Unify (Ashby, live).
				"Are you located in San Francisco or New York City?",
				// FT Partners (Greenhouse, live).
				"Do you live in the San Francisco Bay Area?"},
			Neg: []string{"What city are you currently based in? Are you currently based in a metro?", "Which area are you currently living in? Do you currently live in it?", "Where are you based?", "Do you prefer local candidates?",
				"Are you based in a US or equivalent timezone?", "Do you live in the Americas region?"},
		},
		204: {
			Pos: []string{"Date of birth", "Birth date", "DOB (MM/DD/YYYY)"},
			Neg: []string{"Date of application", "Birthplace"},
		},
	}
}

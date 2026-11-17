package match

// routeCasesFixedA merges the table cases of family A, one map per file.
func routeCasesFixedA() map[int]ruleCases {
	return mergeRuleCases(
		routeCasesFixedASkills(),
		routeCasesFixedAGates(),
		routeCasesFixedAWotc(),
		routeCasesFixedAOffice(),
		routeCasesFixedAConsent(),
		routeCasesFixedAConflict(),
	)
}

func routeCasesFixedASkills() map[int]ruleCases {
	return map[int]ruleCases{
		1: {
			Pos: []string{"What is your preferred tech stack?", "Which technology stack do you prefer to work with?", "Favourite tech stack"},
			Neg: []string{"Do you have experience with our tech stack?", "What is your preferred start date?"},
		},
		2: {
			Pos: []string{"Which AI-specific technologies do you use?", "How comfortable are you with AI/ML technologies?"},
			Neg: []string{"Which AI technologies does the team build?", "Which technologies are you comfortable with?"},
		},
		43: {
			Pos: []string{
				"List the programming languages you know.",
				"Which programming languages are you familiar with?",
				"Programming languages you have experience with",
				"Please share the scripting languages you use",
			},
			Neg: []string{
				"Which programming language do you prefer for the interview?",
				"Describe your preferred programming languages",
				"What is your first spoken language?",
			},
		},
		168: {
			Pos: []string{"Are you comfortable completing a technical assessment?", "Are you willing to take a take-home assignment?", "Would you be open to do a practical exercise?"},
			Neg: []string{"Are you comfortable with a dress code?", "Describe the technical assessment you designed."},
		},
		194: {
			Pos: []string{"Do you have the required years of relevant experience needed for this role?", "Do you have the required software language needed for this position?"},
			Neg: []string{"Do you have the required documents to start?", "Do you have experience needed for this role?"},
		},
		255: {
			Pos: []string{"You acknowledge and agree that you will not use AI tools during the interview.", "I agree not to utilize artificial intelligence in the assessment."},
			Neg: []string{"Do you use AI tools in your daily work?", "During the interview we may use AI to take notes."},
		},
		256: {
			Pos: []string{"During the assessment you agree not to use any artificial intelligence.", "The screening requires that you do not use AI."},
			Neg: []string{"Do you use AI in your daily work?", "We will not record the interview."},
		},
		257: {
			Pos: []string{"We use AI tools to review your profile for future roles.", "Our artificial intelligence matching may consider you for other positions."},
			Neg: []string{"Do you have experience with AI?", "We will contact you about future roles."},
		},
		258: {
			Pos: []string{"I consent to Example Corp processing my data using AI.", "Do you allow us to use your application data with artificial intelligence?"},
			Neg: []string{"Do you consent to the privacy policy?", "I allow Example Corp to process my data."},
		},
		259: {
			Pos: []string{"Other positions may be matched using AI.", "Future openings will be suggested by artificial intelligence."},
			Neg: []string{"Do you consent to the use of data for recruiting?", "We use AI in our products.", "Would you like to be considered for other roles at Scale AI?"},
		},
		261: {
			Pos: []string{"Prompt engineering for AI-related tasks", "AI related tasks"},
			Neg: []string{"Training or fine-tuning AI models for AI-related tasks", "None of the above (AI-related tasks)", "Data entry"},
		},
		279: {
			Pos: []string{"Python: how many years of professional experience?", "Python - years of experience"},
			Neg: []string{"Give examples that illustrate your proficiency in Python, including years of experience", "Years of experience with Python"},
		},
		280: {
			Pos: []string{"How many years of experience do you have with Python?", "Total years of work experience in Python"},
			Neg: []string{"How many years of experience do you have with Java?", "Provide examples of your years of experience in Python and areas used"},
		},
		281: {
			Pos: []string{"Number of years of full time work experience", "Number of Years Full Time Work Experience"},
			Neg: []string{"Number of years of work experience with Example Framework", "Describe your work experience"},
		},
		282: {
			Pos: []string{"Years of experience *", "Years of Experience:", "years of experience"},
			Neg: []string{"Years of experience with Python", "Total years of experience", "Years of experience (optional) in finance"},
		},
		284: {
			Pos: []string{"How many years of experience do you have?", "How many years of professional experience do you have?", "How many years of total experience?",
				// Rain (Ashby, live).
				"How many years of industry experience do you have? Exclude internships or personal projects. Include jobs you've held after graduating university."},
			Neg: []string{"How many years of work experience do you have?", "Years of professional experience",
				"How many years of experience do you have as a financial analyst?"},
		},
		285: {
			Pos: []string{"Years of professional experience", "Please enter your years of relevant experience", "Years of relevant experience required",
				// Glean (Greenhouse).
				"Please input the total years of experience you have that are relevant for this role.", "Total years of experience",
				"Years of professional experience as a whole"},
			Neg: []string{"Years of experience with Java", "How many years of experience do you have?", "What are your total years of experience with Java?",
				"Years of relevant experience as a research scientist"},
		},
		286: {
			Pos: []string{"Years of software engineering experience", "Years of software engineering / product development experience", "Years of programming and technical experience",
				// Modern Health (Greenhouse).
				"How many years of relevant professional software development experience do you have in one or more general-purpose programming languages? (Python, Java, javascript, Go )"},
			Neg: []string{"Years of Example Framework experience", "Years of marketing experience"},
		},
		287: {
			Pos: []string{"How many years of professional work experience do you have?", "How many years of work experience?", "How many years of relevant working experience do you have to the role?"},
			Neg: []string{"How many years of work experience with Example Framework?", "How many years of experience?", "How many years of working experience with Example Framework?",
				"How many years of work experience do you have as a financial analyst?"},
		},
		288: {
			Pos: []string{"Years of work experience", "Years of relevant work experience"},
			Neg: []string{"Years of work experience with Example Framework", "Years of experience"},
		},
	}
}

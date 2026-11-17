package match

func rxbCasesContact() map[int]ruleCases {
	return map[int]ruleCases{
		491: {
			Pos: []string{"Please type your initials", "Enter your signature below"},
			Neg: []string{"Please upload your signature", "Please type the date of your signature"},
		},
		492: {
			Pos: []string{"Electronic Signature", "* Signature", "Applicant's initials", "eSignature"},
			Neg: []string{"Signature date", "Signature file"},
		},
		493: {
			Pos: []string{"Sign your full name", "Name (Signature Field)"},
			Neg: []string{"Sign your name and add today's date", "Print your name"},
		},
		500: {
			Pos: []string{"Your Name", "* Your full name",
				// Rula (Ashby): the whole name asked as its two parts.
				"First and last name (legal name)", "First and last name",
				"By typing your name below, I hereby certify that the answers that I have provided are complete and accurate.", "By entering your full legal name, you acknowledge and agree to the terms above",
				"An employer who violates this law is subject to a fine. By entering your name, you confirm that you have read the above statement:",
				"By typing my name below, I confirm that the facts set forth in this application are true and complete to the best of my knowledge.",
				"So we can pronounce it correctly, what is the phonetic spelling of your name? (e.g., Kristina would be chris-teen-uh)", "How do you pronounce your name?", "Name pronunciation",
				// Block (Greenhouse, live): the signature asked after the declaration, its parts named.
				"I certify that the information I have provided is correct. Please sign by typing your Full Legal First, Middle Initial, and Last Name.",
				// Figure (Greenhouse).
				"Please provide your legal first and last name."},
			Neg: []string{"What is your name?", "Select an option. The name entered is your legal name.", "By typing your name and the date below, you certify the above",
				"Please sign by typing your initials.",
				"How do you pronounce the company's name?", "Please provide your preferred first name.", "Please provide your manager's name."},
		},
		503: {
			Pos: []string{"Street Address", "Address Line 1", "Address 1", "Mailing address"},
			Neg: []string{"Address 10", "Address 1st shift", "Email address"},
		},
		504: {
			Pos: []string{"Street", "Street name"},
			Neg: []string{"Street food preferences", "Favorite street"},
		},
		505: {
			Pos: []string{"Address Line 2", "Apt.", "Suite", "Unit number"},
			Neg: []string{"Address Line 3", "Describe your United States experience", "Describe your experience with unit testing in a large suite of tools", "Do you have unit test experience?"},
		},
		506: {
			Pos: []string{"Are you currently located outside of the United States?", "Do you reside outside the U.S.?"},
			Neg: []string{"Are you currently located in the United States?", "Do you live outside of Springfield?"},
		},
		508: {
			Pos: []string{"City", "Current city", "What city do you live in? (e.g. Salt Lake City)"},
			Neg: []string{"Which city and state do you live in?", "* State (US) * City (US)", "City where you are currently located", "Is your city near an in-person office?",
				// Character (Ashby): the word is a place's name.
				"Are you open to working full-time in our New York or Redwood City, CA offices?", "Can you commute to our New York City office?"},
		},
		509: {
			Pos: []string{"Location", "Location (City)", "Current location", "Current Location We use this for payroll.",
				"Location Let us know your place of residence so that we can review local hiring requirements.",
				// Apex (Ashby, live): a note that offers relocation assistance asks nothing of it; Lime's residence.
				"Location We work on-site in Playa Vista, Los Angeles. If you're not in LA, let us know. We offer relocation assistance.",
				"Location of Residence",
				// Infinitus (Ashby, live): a location typeahead.
				"Primary Location",
				// Hopper (Ashby, live): a bracketed note on why the place is asked.
				"Current Location (Despite our remote first culture, some teams hire only in specific timezones!)"},
			Neg: []string{"Preferred location", "Why this location?", "Location We use this to pick an office.", "Location Please tell us if you are open to relocating.",
				"Location (remote/hybrid)", "Current location (are you open to relocating?)", "Current location (city) Remote or office?",
				"Primary work location", "Primary location preference"},
		},
		510: {
			Pos: []string{"Where do you currently reside?", "Where do you live?", "From where do you intend to work?", "Where will you be working from?",
				"Where do you plan to be based?", "Where do you live? We are a remote-first team.", "Location Where is your specific working location?",
				"What is your place of residence?",
				// Temporal (Ashby, live): Ashby's own location field under its own question.
				"What location do you intend to work from?", "What city will you be working from?",
				// incident.io (Ashby, live): the same field.
				"What is your location?",
				// Aurora Solar (Ashby, live): the same field again.
				"Please note your working location:", "Please share your current work location",
				// SurveyMonkey (Ashby, queue154): the same field.
				"In which city are you located?", "Which city are you currently based in?"},
			Neg: []string{"Do you currently reside in Exampleland?", "Are you currently located outside the US?", "Do you intend to work remotely?",
				"Are you located in Kansas City?", "Which city are you willing to relocate to?",
				"What is your location preference?",
				"Where do you intend to work: remote, hybrid or in the office?", "In which country will you work?",
				"What is your preferred working location?", "What is your country of residence?",
				"Which office location do you intend to work from?", "What location do you intend to work from: remote or the office?",
				"Please note your preferred working location:"},
		},
		511: {
			Pos: []string{"Do you intend to work in the state where this job is posted?", "Do you intend to work in this state?"},
			Neg: []string{"Do you intend to work remotely?", "Which state do you live in?"},
		},
		514: {
			// Coalition (Greenhouse, live): "Zipcode" as one word.
			Pos: []string{"Zip Code", "Postal code", "Postal", "Postcode", "Zipcode"},
			Neg: []string{"Zip+4", "Zip plus 4", "Postal address"},
		},
		516: {
			Pos: []string{"County"},
			Neg: []string{"Country"},
		},
		523: {
			// Outtake (Ashby, live): a bare "Contact Number" is the phone.
			Pos: []string{"Phone number", "Mobile", "Cell phone", "Contact Number", "Contact no.", "Best contact number"},
			Neg: []string{"Email address", "Preferred contact time", "Emergency contact number", "Contact number of your reference",
				// Ramp (Ashby, live): mobile engineering is a field of work, not a phone.
				"Do you have a minimum of 2 years of mobile engineering experience, not including internships?"},
		},
		524: {
			Pos: []string{"LinkedIn profile URL", "How many LinkedIn profile links do you have?"},
			Neg: []string{"How many people do you know on LinkedIn?", "Your resume"},
		},
		525: {
			Pos: []string{"GitHub username", "How many GitHub links can you share?"},
			Neg: []string{"How many GitHub repositories do you maintain?", "GitLab username"},
		},
		526: {
			Pos: []string{"Portfolio", "Personal website", "Personal site"},
			Neg: []string{"How many portfolio pieces can you show?", "Company site", "Are you currently employed by a Vista portfolio company?"},
		},
		527: {
			Pos: []string{"Website URL", "Website link"},
			Neg: []string{"Company website", "Website"},
		},
		528: {
			Pos: []string{"Website", "Website *"},
			Neg: []string{"Company website", "Website, Blog or Portfolio"},
		},
		529: {
			Pos: []string{"Twitter", "Twitter handle"},
			Neg: []string{"Telegram handle"},
		},
		530: {
			Pos: []string{"x . com", "X.com URL"},
			Neg: []string{"Do you have a box.com account?", "Dot com experience"},
		},
		531: {
			Pos: []string{"X profile URL", "X handle"},
			Neg: []string{"X", "Which x axis do you prefer?"},
		},
		532: {
			Pos: []string{"Other website", "Other site URL", "Other URL"},
			Neg: []string{"Company website", "Other languages", "Other links to your work: describe each"},
		},
		560: {
			Pos: []string{"Are you currently being sponsored by another company in the US?", "Is your visa sponsored by your current employer?"},
			Neg: []string{"Will you now or in the future require sponsorship?", "Are you a sponsor of a student organization?"},
		},
		561: {
			Pos: []string{"Which of the following programming languages are you proficient in?", "Which of the following languages do you consider yourself professionally proficient in?", "Select the tools you have worked with"},
			Neg: []string{"Which of the following languages do you speak proficiently?", "Which programming language do you like most?", "What is your native language?", "Which data platforms have you used in production?"},
		},
		563: {
			Pos: []string{"Select what you have experience working in:", "Check all you have experience with",
				"Which of the following do you have experience working with directly?", "Which of the following have you experience with?"},
			Neg: []string{"Select your years of experience", "Select what you have experience working in and describe it in detail. How long?", "What do you have experience working in?"},
		},
		573: {
			Pos: []string{"Type to Add Skills", "Skills*", "Add Skills"},
			Neg: []string{"Soft skills you bring to the team", "Which skills make you a fit for this role?", "Skills assessment completed?"},
		},
		536: {
			Pos: []string{"Share links to any personal projects, open-source contributions, or apps you've built"},
			Neg: []string{"Describe your proficiency in Example Language, including years of experience and areas used, e.g. coursework, internships, personal projects", "Do you enjoy side hobbies?",
				// Rain (Ashby, live): personal projects excluded from a count of years.
				"How many years of industry experience do you have? Exclude internships or personal projects. Include jobs you've held after graduating university."},
		},
		551: {
			Pos: []string{"Do you have the ability to work in a team?", "Do you have a strong understanding of testing?"},
			Neg: []string{"Do you have the ability to work legally in Exampleland?", "Do you have a certified skill in welding?", "Do you have knowledge of Example Framework for 5 years?"},
		},
		552: {
			Pos: []string{"Are you able to work in a fast-paced setting?", "Are you comfortable to work with a team?"},
			Neg: []string{"Are you able to work in the country legally?", "Are you able to travel for work?"},
		},
		553: {
			Pos: []string{"Are you ready to work in a fast-paced environment?", "Are you excited to work long hours?"},
			Neg: []string{"Are you ready to work overtime hours?", "Are you ready to work?"},
		},
	}
}

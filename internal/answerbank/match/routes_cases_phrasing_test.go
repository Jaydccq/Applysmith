package match

func routeCasesPhrasing() map[int]ruleCases {
	return map[int]ruleCases{
		59:  {Pos: []string{"Current Title", "What is your present title?", "Current or Most Recent Title", "Most recent title", "Current/Previous Title"}, Neg: []string{"Current title company", "Preferred title of address", "Most recent title held at your current employer"}},
		68:  {Pos: []string{"US Work Authorization", "United States work authorization *"}, Neg: []string{"Are you authorized to work in the US? US work authorization expires"}},
		109: {Pos: []string{"Visa Support", "Sponsorship", "Visa Sponsorship *", "Visa support needed"}, Neg: []string{"Do you require visa sponsorship now or in the future?"}},
		121: {Pos: []string{"Office of choice", "Are you working from any of the following offices?"}, Neg: []string{"Is the office dog friendly?"}},
		562: {Pos: []string{"Where will you work? Our Lisbon roles are in office 4 days a week.", "Pick one. Choose the office listed on this job posting."}, Neg: []string{"Where did you work last?", "Will you work weekends?", "Choose the date of your interview"}},
		132: {Pos: []string{"Would you consider relocating to Example City?"}, Neg: []string{"Would you consider a relocation package?", "Do you need relocation assistance when considering relocation?"}},
		143: {Pos: []string{"Are you located in Springfield or able to be based in Springfield to work on-site?"},
			Neg: []string{"Are you based in Springfield today?", "Are you willing to relocate?", "Are you based in Springfield or willing to relocate to work hybrid?"}},
		203: {Pos: []string{"Proficiency in Spanish", "French language fluency", "Written level"}, Neg: []string{"Proficiency in Python", "Your written communication style"}},
		230: {Pos: []string{"Salary range requirements", "Compensation range expectation"}, Neg: []string{"What is the salary range of the job?", "What are your hourly pay range expectations?"}},
		245: {Pos: []string{"If you selected referral, please provide the name"}, Neg: []string{"Referral bonus policy"}},
		267: {Pos: []string{"I agree that Example Corp may contact you about other job opportunities"}, Neg: []string{"May we contact your references?"}},
		303: {Pos: []string{"Do you identify as having a disability?", "If you believe you have a disability as defined above, please indicate by making the appropriate selection below."},
			Neg: []string{"Do you identify as someone with a disability needing reasonable accommodation?", "If you believe you have a disability that needs a reasonable accommodation, contact us."}},
		333: {Pos: []string{"Type of business", "Business type *"}, Neg: []string{"What type of business plan do you prefer to write?"}},
		403: {Pos: []string{"Is your current or former employer a customer of Example Corp?", "Is your current employer a customer of Example Corp?",
			"Are you aware if your current employer has a relationship with Example Corp involving the delivery of services?"}, Neg: []string{"Who is your current employer?"}},
		406: {Pos: []string{"Work authorization expiration date", "Employment authorization end date"}, Neg: []string{"Are you authorized to work?"}},
		490: {Pos: []string{"Location of Residence and Language:", "Place of residence"}, Neg: []string{"Residence permit number", "Location of your last employer"}},
		502: {Pos: []string{"Preferred name", "Preferred first name (optional)"}, Neg: []string{"Preferred last name", "Preferred name pronouns"}},
		522: {Pos: []string{"Email", "Primary email address", "Your e-mail *", "Personal email ID",
			// Institute of Foundation Models (Lever, owner workspace).
			"Please enter your personal email ID (If you havent already)"},
			Neg: []string{"What is your email provider's policy for forwarding?", "Email notifications opt in",
				"Please enter your manager's email address", "Please provide your email address if you would like updates"}},
	}
}

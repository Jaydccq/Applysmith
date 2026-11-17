package match

func rxbCasesLocation() map[int]ruleCases {
	return map[int]ruleCases{
		28: {
			Pos: []string{"Can you travel for work?", "Are you able to travel?"},
			Neg: []string{"Can you start on Monday?", "Are you willing to travel?"},
		},
		122: {
			Pos: []string{"Are you currently located in Springfield or willing to relocate?", "Are you willing to relocate, or are you currently based in Springfield?", "Are you local to Springfield or willing to relocate?", "Are you currently located in or able to commute to Springfield at least 2 days/week?",
				// Brex (Greenhouse, live).
				"Do you currently live in, or plan to relocate to, the specified location to meet this in-office requirement?"},
			Neg: []string{"Are you currently located in Springfield?", "Are you willing to relocate?"},
		},
		123: {
			Pos: []string{"If you're not currently based in Springfield, are you open to relocating?"},
			Neg: []string{"If you are located in Springfield, please select an office.", "Are you open to relocating?"},
		},
		125: {
			Pos: []string{"Are you currently in Springfield and willing to work onsite?", "Are you in Exampleville & willing to come into the office in-person?",
				// Ramp (Ashby, live).
				"Are you currently based out of the New York City or San Francisco area, and able to work out of our office 2-3 days per week?"},
			Neg: []string{"Which city are you in and willing to work onsite?", "Are you in the US and willing to work onsite?", "Are you currently in school and willing to work hybrid?", "Are you in Springfield and willing to work remotely?"},
		},
		126: {
			Pos: []string{"Are you willing to relocate?", "Would you be willing to consider relocation?",
				// Relativity Space (Greenhouse, owner workspace).
				"Are you willing to commute and/or relocate for this role?"},
			Neg: []string{"Are you able to relocate?", "Are you willing to travel?", "Are you willing to commute to our office?"},
		},
		127: {
			Pos: []string{"Are you open to relocating?", "Are you open to considering relocation?"},
			Neg: []string{"Are you willing to relocate?", "Are you open to remote work?"},
		},
		128: {
			Pos: []string{"Are you able to relocate?", "Are you prepared to relocate?"},
			Neg: []string{"Are you open to relocate?", "Are you able to travel?"},
		},
		129: {
			Pos: []string{"Are you willing to locate to Springfield?"},
			Neg: []string{"Are you willing to relocate?"},
		},
		130: {
			Pos: []string{"Are you open to locate near Springfield?"},
			Neg: []string{"Are you open to remote work?"},
		},
		131: {
			Pos: []string{"Are you willing to move to Springfield?", "Are you open to moving to Springfield?", "Are you willing to work from the required location?",
				"Are you able to work from the stated location of this role?"},
			Neg: []string{"Are you willing to move on quickly?", "Are you willing to work from the office 3 days a week?"},
		},
		133: {
			Pos: []string{"Will you require relocation?", "Do you currently need to relocate?"},
			Neg: []string{"Will you require relocation assistance?", "Do you require relocation or are you local to Springfield?", "Will you require a laptop?"},
		},
		172: {
			Pos: []string{"Are you open to remote work?", "Are you open to working remotely?"},
			Neg: []string{"Are you open to relocating?"},
		},
		176: {
			Pos: []string{"Are you willing to travel?", "Are you open to travel?", "Are you open to domestic travel of approximately 50%?"},
			Neg: []string{"Are you willing to relocate?"},
		},
		// n8n (Ashby, live): the share of time the role travels, then whether the applicant is able or willing.
		587: {
			Pos: []string{"In what countries do you have the unrestricted right to work?", "In which countries are you authorized to work?",
				"Which countries do you have the legal right to work in?", "In which countries are you authorized to work without sponsorship?"},
			Neg: []string{"Which country do you currently reside in?", "In which country will you work?", "What countries have you lived in?"},
		},
		585: {
			Pos: []string{"May we contact your current employer?", "Can we reach out to your present supervisor?", "Do you authorize us to contact your current employer?"},
			Neg: []string{"Do you have obligations to a current employer that would limit this role?", "Who is your current employer?",
				"Please provide the contact information of your current employer", "May we contact your references?"},
		},
		584: {
			Pos: []string{"Which location are you applying for?", "What office location are you applying to?", "Which locations are you applying for?"},
			Neg: []string{"Which role are you applying for?", "Which location do you prefer?", "What location are you based in?"},
		},
		583: {
			Pos: []string{"Are you authorized to work in the country you currently reside in?", "Are you legally authorized to work in your country of residence?",
				"Are you eligible to work in the country where you live?", "Are you legally authorized to work in the country you are based in?",
				"Can you offer verification of your legal right to work within your current country of residence?",
				"Are you legally authorized to work in your current country of employment?",
				"Are you legally eligible to work in the country where you're planning to work from?"},
			Neg: []string{"Will you require sponsorship to be authorized to work in the country you reside in?", "Which country do you currently reside in?",
				"Are you authorized to work in the country where this job is based?"},
		},
		582: {
			Pos: []string{"AI Policy for Application We invite you to review our AI partnership guidelines for candidates and confirm your understanding by selecting \u201cYes.\u201d",
				"Please acknowledge the policy above by answering \"Yes\".", "Confirm that you have read the handbook by selecting Yes.",
				"Please select \"Yes\" to confirm your understanding and agreement.", "Answer Yes to confirm that you have read the policy."},
			Neg: []string{"Please indicate whether you require sponsorship by selecting Yes or No.", "Confirm your start date by selecting a date.", "Select Yes if you are over 18.",
				"Select Yes to confirm you require sponsorship."},
		},
		581: {
			Pos: []string{"Are you authorized to work in the stated location of this role?", "Are you legally authorized to work in this role's location?", "Are you eligible to work in the listed location of the position?",
				"Are you legally authorized to work in the location where this role is based?"},
			Neg: []string{"Will you require sponsorship to work in the stated location of this role?", "Are you able to work from the stated location of this role?", "Are you authorized to work in the United States?"},
		},
		588: {
			Pos: []string{"Our interviews require you to be on video. Can you confirm that you can keep your camera on for the entire duration of the interview?", "Are you comfortable having your camera on during video interviews?"},
			Neg: []string{"Do you have a working camera?", "Do you consent to the interview being recorded with your camera on?"},
		},
		589: {
			Pos: []string{"I consent to participate in an in-person interview or facial ID verification if required for this role.", "Facial identity verification may be part of our process. Do you agree?",
				"Due to the sensitive nature of our work at Paxos, we may conduct identity verification during the interview process. This may include validating government-issued identification and confirming candidate location. Do you understand and consent to this process?"},
			Neg: []string{"I consent to a background check, including identity verification.", "Are you willing to travel for an interview?", "Do you consent to the interview being recorded?"},
		},
		590: {
			Pos: []string{"Are you interested in working from one of our offices?", "Would you be interested in working in our HQ?"},
			Neg: []string{"If you answered 'Yes' to working from one of our offices, which office are you interested in working from?", "Are you interested in working remotely?", "Which of our offices are you interested in?"},
		},
		594: {
			Pos: []string{"Share one link to a page or project you built that showcases why you'd be great at this role.", "Link to a website you have built"},
			Neg: []string{"Please share a link to a mobile product that you have built.", "Link to a project you would like to build",
				"How many links to a page you built can you share?"},
		},
		// Range (Ashby, live): five office days, with relocation to a hub if needed.
		598: {
			Pos: []string{"Can you commit to being in office Monday - Friday? This role is based out of our headquarters in McLean, VA or one of our hubs in New York City or Seattle. If you're not currently located in one of these areas, your willingness to relocate is needed for employment.", "Can you commit to working in the office Monday through Friday?", "Are you able to be in-office Monday to Friday?"},
			Neg: []string{"Do you work Monday to Friday?", "Can you commit to a start date?", "Which office would you commit to?"},
		},
		600: {
			Pos: []string{"Are you aware that Camber is based in New York City and looking for team members to work hybrid from our office in Flatiron?",
				"Do you understand that this role is on-site at our San Francisco office?"},
			Neg: []string{"Are you aware of our hybrid work policy?", "Are you aware that our office is closed on Fridays?", "Do you understand our product?"},
		},
		// Owner decisions: on-call, Security+, the job
		// search timeline, the working hours, English at work and a list of
		// public employers.
		605: {
			Pos: []string{"Are you comfortable participating in an on-call rotation and troubleshooting production data pipeline failures?",
				"On-Call Requirements: Are you willing and able to participate in this job's on-call requirements?", "Will you participate in the on-call schedule?"},
			Neg: []string{"What on-call compensation are you willing to accept?", "Describe your on-call experience.", "Is this role on-call?"},
		},
		// DuckDuckGo (Ashby, queue164): learning the role's languages is a
		// duty of the role.
		615: {
			Pos: []string{"Though we're open to hiring engineers from any language background, most of our backend work is done in Perl (version 5), with some Node.js and Go. Please confirm you're open to learning and using any of these languages as needed",
				"Are you willing to learn new tools and technologies?", "Are you open to learning Rust?", "Are you open to learning C++?"},
			Neg: []string{"Are you open to learning about our benefits?", "What languages are you learning?", "Are you willing to learn a new spoken language for this role?",
				"Are you open to relocating?"},
		},
		// Coinbase (Greenhouse, queue164): the applicant as an official.
		614: {
			Pos: []string{"Are you a current government official or were you a government official in the last five years (e.g., employee of a government agency or a government owned/controlled company, holder of public office or a civil service position)?",
				"Were you ever a government official?", "Have you been a government official?"},
			Neg: []string{"Is a government official reading this?", "Are you a government contractor?", "Did a government official refer you?"},
		},
		// Everlywell (Lever, queue154): a duty of the role itself.
		613: {
			Pos: []string{"Are you comfortable supporting executives and handling urgent IT requests?",
				"Would you be comfortable working directly with customers on technical issues?", "Are you comfortable juggling multiple priorities?",
				"Are you comfortable supporting executives at Hippocratic AI?",
				"Are you comfortable regularly communicating and collaborating with cross-functional teams, including non-technical stakeholders such as GTM, Product, and Operations?"},
			Neg: []string{"Are you comfortable lifting and handling equipment up to 50 lbs?", "Are you comfortable handling urgent requests on weekends and holidays?",
				"Are you comfortable with the pay range for this role?", "Are you comfortable working from our Austin office?", "Is your manager comfortable supporting you?",
				"Are you comfortable working with AI tools?"},
		},
		607: {
			Pos: []string{"Do you have an active Security+ certification?", "Do you hold CompTIA Security Plus?"},
			Neg: []string{"Are you willing to obtain Security+ within 6 months of hire?", "Do you have Security+ or an equivalent certification?",
				"Do you hold a CISSP or Security+?", "Describe your security experience."},
		},
		608: {
			Pos: []string{"What is your current job search timeline? Are there any deadlines or competing processes we should be aware of?",
				"Are you in other interview processes right now?", "What is your job search status?"},
			Neg: []string{"Do you have any competing offer deadlines?", "Describe your hiring process experience.", "Search engine experience"},
		},
		609: {
			Pos: []string{"We are a distributed company. What are your preferred working hours, and what hours are you willing to work? Please include your time zone.",
				"What are your typical work hours?"},
			Neg: []string{"How many hours per week can you work?", "What are your preferred working hours per week?", "Which shifts are you available to work?"},
		},
		610: {
			Pos: []string{"Are you fluent in English?", "Are you able to communicate effectively in English, both verbally and in writing, in a professional work environment?",
				"Are you proficient in Spanish?"},
			Neg: []string{"Do you speak any languages other than English?", "Are you able to communicate effectively with customers?", "Are you fluent in Python?"},
		},
		611: {
			Pos: []string{"Have you ever been directly employed by (1) any government or military entity, state-owned enterprise, or publicly-funded institution, or (2) a government contractor in a role that recommended Snowflake as part of Government procurement?",
				"Have you worked for a government agency or a publicly funded institution?"},
			Neg: []string{"Has any member of your immediate family been employed by a government or a state-owned enterprise?", "Have you worked for a state-owned enterprise?",
				"Is your current employer a government agency?"},
		},
		// Everlywell (Lever, queue154): the employment alone; its narrowing by
		// "and been involved in procurement" is the narrowed-disclosure stage's.
		612: {
			Pos: []string{"Have you ever worked for a state agency?", "Have you been employed by a federal agency?"},
			Neg: []string{"Have you ever worked for a State agency and been involved, in any capacity, in the process of procurement of goods or services?",
				"Have you ever worked for a staffing agency?", "Has any member of your immediate family worked for a state agency?"},
		},
		// Antithesis (Ashby, live): the fully on-site arrangement stated, then the willingness asked.
		604: {
			Pos: []string{"Given how quickly we're scaling and the level of collaboration this work requires, this role is fully onsite with the team. Are you willing to commit to this?",
				"This position is 100% in-office. Are you comfortable with that?"},
			Neg: []string{"This role is fully onsite in Vienna, VA. Are you willing to relocate?", "This role is fully onsite. Are you located in Vienna, VA?",
				"Are you fully onsite in your current role?"},
		},
		603: {
			Pos: []string{"Before we proceed with your application, we would like to ensure that you are comfortable with us collecting and processing your personal information for the purpose of this application and potential employment. Your data will be handled in accordance with Kin's applicant privacy policy here .",
				"We will store your personal data as described in our privacy notice."},
			Neg: []string{"We will store your personal data for future job opportunities as described in our privacy notice.", "Do you collect personal information in your current role?"},
		},
		602: {
			Pos: []string{"Please confirm you have read DNSFilter's AI Usage in Hiring Practices notice (see the \"Learn more\" link below the Submit button).",
				"Please confirm that you have read our candidate disclosure statement."},
			Neg: []string{"Have you read our privacy notice?", "Please confirm that you have read the entire job description.", "Please confirm you have read the notice and opt out of AI screening."},
		},
		599: {
			Pos: []string{"I understand that this role requires twice-yearly travel for company offsites:", "I acknowledge this position requires occasional travel.",
				"I understand and agree that the role involves travel to customer sites"},
			Neg: []string{"I understand that this role requires 50% travel.", "I understand that this role is fully remote.", "I understand that travel expenses are reimbursed."},
		},
		597: {
			Pos: []string{"Are you able to meet in person at least twice a year at an offsite?", "Are you able to attend in-person company offsites twice a year?",
				"Would you be available to join our team offsites?"},
			Neg: []string{"Where was your last team offsite?", "Have you organized an offsite before?", "Are you able to work onsite?"},
		},
		596: {
			Pos: []string{"How well do you know us? Please select all of the following sources/tools that you have used to research KnowBe4 and learn more about us.",
				"Which resources did you use to research our company?", "How did you research us?"},
			Neg: []string{"How did you hear about us?", "How did you learn about this job?", "Which tools do you use to research market trends?"},
		},
		595: {
			Pos: []string{"Will the target salary range listed ($145,600 - $163,800) meet your salary requirements?", "Does the posted pay band match your expectations?"},
			Neg: []string{"What salary range meets your requirements?", "Will the role meet your career requirements?"},
		},
		593: {
			Pos: []string{"We welcome responsible AI use. Did you use an AI tool to tailor your résumé, write responses, or submit this application?", "Was this application completed with the help of ChatGPT?", "Have you used generative AI to fill out this form?"},
			Neg: []string{"Will you use AI tools during the coding interview?", "Do you have experience building generative AI applications?", "Do you consent to AI note-taking during interviews?"},
		},
		592: {
			Pos: []string{"If you are authorized to work in the United States, is that authorization pursuant to an Employment Authorization Document (EAD)? If yes, please indicate the applicable EAD category code.", "EAD category code", "What category is listed on your employment authorization card?"},
			Neg: []string{"Does your work authorization depend on an Employment Authorization Document?", "When does your EAD expire?", "Which job category are you applying for?"},
		},
		591: {
			Pos: []string{"Are you able to conduct a coding and system design interview?", "Are you comfortable with a live coding interview?", "Can you do a technical interview in Python?",
				"Are you able to complete a coding interview with the Fireworks AI engineering team?"},
			Neg: []string{"Will you use AI tools during the coding interview?", "Do you consent to the technical interview being recorded?", "Describe a system design you are proud of."},
		},
		622: {
			Pos: []string{"Are you willing to share your academic transcripts?", "Are you able to provide official transcripts upon request?",
				"Can you submit unofficial transcripts if requested?"},
			Neg: []string{"Please upload a copy of your transcript.", "Please provide the GPA shown on your transcript.",
				"Do you have experience transcribing interviews?", "Are you willing to share your portfolio?"},
		},
		// The owner's certifications list: a certification held, or the list;
		// earning one, a license and certifying a statement are other questions.
		627: {
			Pos: []string{"Do you have an AWS certification?", "Do you hold a PMP certification?", "Do you have any certifications?",
				"Are you AWS certified?", "Which certifications do you hold?", "Certifications"},
			Neg: []string{"Are you willing to obtain a PMP certification?", "Do you hold a professional license?",
				"I certify that the information I provided is true", "Do you have a certificate of completion from a bootcamp you can upload?",
				"Do you have the certifications required for this role?", "Do you have any relevant certifications?"},
		},
		// A licence, charter or registration the question names; one described
		// in common words, a driver's licence and a registry are other questions.
		641: {
			Pos: []string{"Do you hold the Series 7 license?", "Are you a licensed CPA?", "Are you a CFA charterholder?", "Are you Series 66 licensed?"},
			Neg: []string{"Do you have a valid license for this state?", "Are you licensed to practice in New York?", "Do you hold a valid driver's license?",
				"Are you willing to obtain your Series 7 license?", "Are you registered as disabled under the Equality Act?"},
		},
		// Vultr (Ashby): a job now, with no employer named.
		601: {
			Pos: []string{"Are you currently employed?", "Are you employed at the moment?"},
			Neg: []string{"Are you currently employed by Example Corp?", "Are you currently employed or a student?", "Have you ever been employed by us?"},
		},
		// Abridge, the Institute of Foundation Models: a scholarly profile and
		// the publications themselves.
		628: {
			Pos: []string{"Google Scholar/Publications", "Google Scholar URL", "Link to your scholarly citation profile"},
			Neg: []string{"Are you a scholarship recipient?", "Portfolio URL", "What school did you attend?"},
		},
		629: {
			Pos: []string{"Publications", "What machine learning research publication are you most proud of, where you were the first author?",
				"Do you have any published papers?", "Please list your publications"},
			Neg: []string{"How did you hear about this job? (e.g. a publication)", "Have you read our blog?", "Do you have a portfolio?"},
		},
		// A competing offer's deadline; whether one is held is route 572.
		630: {
			Pos: []string{"If yes, when is your offer deadline?", "What is the deadline for your other offer?"},
			Neg: []string{"Do you have any competing offer deadlines?", "Do you have any pending offers or deadlines we should know about?"},
		},
		// 15Five (Lever): base pay or on-target earnings.
		631: {
			Pos: []string{"Does your range reflect Base Pay or On-Target Earnings", "Is that base salary or OTE?"},
			Neg: []string{"What is your expected base salary?", "What are your on-target earnings expectations?"},
		},
		633: {
			Pos: []string{"This position will require occasional night shifts. Are you willing and able to work night shifts as required?", "Are you available to work overnight shifts?"},
			Neg: []string{"Which shifts are you available to work?", "Do you prefer to work night shifts?", "Have you worked night shifts before?"},
		},
		634: {
			Pos: []string{"What type of companies have you worked at?", "Which stages of company have you worked at?"},
			Neg: []string{"What type of company do you want to work at?", "Which companies have you worked at?"},
		},
		635: {
			Pos: []string{"What type of products have you worked on previously?", "What kinds of products have you built?"},
			Neg: []string{"What type of products do you use?", "Which product do you like most?"},
		},
		636: {
			Pos: []string{"What type of content do you have experience creating?", "What kinds of content have you created?"},
			Neg: []string{"What type of content do you enjoy reading?", "Describe the content of your thesis."},
		},
		// Applied Intuition (Ashby, owner workspace): an immigration case in
		// progress; the visa held and a past case are other questions.
		626: {
			Pos: []string{"Do you currently have an active immigration case (ex H-1B extension, green card)?",
				"Do you have a pending green card petition?", "Is there an open immigration petition filed on your behalf?"},
			Neg: []string{"What is your current immigration status?", "Will you require visa sponsorship?",
				"Have you ever been denied a visa?"},
		},
		// Skydio (Ashby, owner workspace): the drone pilot certificate held; being
		// able to get one is another question.
		625: {
			Pos: []string{"Do you currently have your FAA Part 107?", "Do you hold an FAA Part 107 certificate?", "Do you have a Part 107 remote pilot certificate?"},
			Neg: []string{"Do you currently have a Part 107 or would be able to get one for this role?", "Are you familiar with FAA regulations?",
				"Do you have experience flying drones?"},
		},
		// Alnylam (Greenhouse, owner workspace): the shifts to consider.
		624: {
			Pos: []string{"Which of the following shifts should we consider you for? (Select all that apply)", "What shifts can you work?"},
			Neg: []string{"Which of the following shifts in the market concern you most?", "Describe a shift of strategy you led.",
				"Which shift pattern would you prefer?", "Which work shift do you prefer?",
				"Are you comfortable with a fast-paced environment?"},
		},
		// Wonderschool (Greenhouse, owner workspace): interest in a role at a
		// named place is the willingness to work there.
		623: {
			Pos: []string{"Are you interested in a substitute teaching position in Northeast Wisconsin?",
				"Would you be interested in a hybrid role based in Austin, TX?"},
			Neg: []string{"Are you interested in a position in sales?", "Are you interested in this role?",
				"Are you interested in a role in Engineering Management?"},
		},
		618: {
			Pos: []string{"Are you applying via an automated script or spoofing?", "Are you submitting this application using a bot?",
				"Is this application being completed by automated software?", "Are you applying to Fireworks AI via an automated script?"},
			Neg: []string{"Did you use AI tools to help complete this application?", "Do you have experience writing automated scripts?",
				"Have you built bots or automation tools?", "Are you applying via an automated script built with Generative AI?"},
		},
		619: {
			Pos: []string{"Are you admitted to practice law in any U.S. jurisdiction?", "Are you a member in good standing of the California State Bar?",
				"Are you a licensed attorney?", "Do you hold an active bar membership?",
				"Are you currently admitted to and in good standing with at least one US state or federal bar?"},
			Neg: []string{"In which states are you admitted to practice law?", "What year were you admitted to the bar?",
				"Do you have experience working with attorneys?", "Have you worked at a bar or restaurant?"},
		},
		620: {
			Pos: []string{"Current Salary", "What is your current salary?", "Current annual base salary (USD)"},
			Neg: []string{"What is your current monthly net salary?", "What are your salary expectations?", "Current salary expectations",
				"What is your current total compensation?", "Current hourly salary"},
		},
		-1: {
			Pos: []string{"Which languages do you speak?", "Please select all the languages you speak fluently.", "Languages Spoken",
				"What languages are you fluent in?"},
			Neg: []string{"Which programming languages do you use?", "Please list any other languages you speak and their level using the CEFR scale.",
				"Do you speak another language?", "Rate your English", "What other languages do you speak?", "Which languages do you speak besides English?"},
		},
		617: {
			Pos: []string{"Have you worked at a startup or quant firm? (i.e., internship, part-time, etc)", "Have you worked at an early-stage startup?",
				"Do you have experience working at a startup?"},
			Neg: []string{"Have you founded a startup?", "How many years have you worked at startups?", "Why do you want to work at a startup?",
				"Have you worked at an early-stage startup for 6+ months?"},
		},
		// Oklo (Greenhouse, queue173): the coding problem after the interview.
		616: {
			Pos: []string{"If selected for a preliminary interview, you may be asked to work through a Python coding problem. Do you acknowledge this?",
				"Our interviews include a live coding exercise. Are you comfortable with this?"},
			Neg: []string{"Will you use AI to solve the coding problem during the interview? Do you acknowledge this policy?", "Describe a coding problem you solved in a past interview.",
				"Do you acknowledge that the interview will be recorded while you work through a coding problem?"},
		},
		580: {
			Pos: []string{"What's the name you'd prefer us to use throughout the interview process?", "What name do you go by?", "Which name would you like us to call you?"},
			Neg: []string{"What is your legal name?", "What last name would you prefer us to use?", "How do you pronounce your name?", "What name would you like for the team?"},
		},
		579: {
			Pos: []string{"Where did you attend and graduate from your undergraduate studies?", "Which university did you attend for your undergraduate degree?", "Undergraduate university"},
			Neg: []string{"What was your undergraduate major?", "What undergraduate degree do you hold?", "When did you complete your undergraduate studies?"},
		},
		578: {
			Pos: []string{"We require all employees onsite, in-person. Are you able to accommodate an onsite full-time work schedule?", "Are you comfortable with an in-office work schedule?"},
			Neg: []string{"What onsite schedule would you prefer?", "Are you able to accommodate a flexible work schedule?"},
		},
		575: {
			Pos: []string{
				"This role involves travelling approximately 30–50% of the time, primarily across the US, including focused on-site customer engagements. Are you able and willing to meet this requirement?",
				"This role requires up to 20% travel. Are you able to meet this requirement?",
				"Travel is expected 25% of the time; would you be willing?",
			},
			Neg: []string{"Are you able to work 100% remote?", "This role reimburses 20% of travel costs.",
				"What percentage of the time are you able to travel?"},
		},
		177: {
			Pos: []string{"Are you comfortable with travelling?", "Are you comfortable with travel?"},
			Neg: []string{"Are you comfortable with deadlines?"},
		},
		178: {
			Pos: []string{"Do you have any impediments to traveling?"},
			Neg: []string{"Do you have any impediments to working?"},
		},
		179: {
			Pos: []string{"Are you open to occasional travelling?", "Are you open to traveling?"},
			Neg: []string{"Are you open to remote work?"},
		},
		180: {
			Pos: []string{"Frequent travel is required. Do you agree?"},
			Neg: []string{"Do you like to travel?"},
		},
		181: {
			Pos: []string{"Travel availability"},
			Neg: []string{"Availability to start"},
		},
	}
}

func rxbCasesEmployment() map[int]ruleCases {
	return map[int]ruleCases{
		30: {
			Pos: []string{"Have you ever interviewed with Example Corp?", "Have you previously been interviewed here?"},
			Neg: []string{"Have you ever interviewed candidates for Example Corp?", "Are you available to interview for this role?"},
		},
		41: {
			Pos: []string{"Currency", "Salary currency", "Preferred currency", "What currency?"},
			Neg: []string{"Salary Expectation - Currency", "Currency exchange experience"},
		},
		44: {
			Pos: []string{"Do you have relatives employed by Example Corp?", "Do you have a close relative (spouse, child, sibling, etc.) who is currently employed at Example Corp?"},
			Neg: []string{"Do you have relatives in Springfield?", "Do you have relatives in Springfield, Oregon, etc. Where do you work?"},
		},
		45: {
			Pos: []string{"Do you have any family members who are employees of Example Corp?"},
			Neg: []string{"Are you related to a famous person?"},
		},
		46: {
			Pos: []string{"Are you related to any Example Corp associate?", "I am related to a current employee of the company"},
			Neg: []string{"Are you related to a famous person?"},
		},
		47: {
			Pos: []string{"Are you related to anyone who is currently working for Example Corp?"},
			Neg: []string{"Are you related to anyone famous?"},
		},
		48: {
			Pos: []string{"Is your spouse currently employed by Example Corp?"},
			Neg: []string{"Is your spouse a teacher?", "Is a family member employed by a political party?"},
		},
		49: {
			Pos: []string{"Do you have a conflict of interest with a relative at Example Corp?"},
			Neg: []string{"Do you have a conflict with the schedule?", "Do you have a conflict of interest?",
				"To avoid a conflict of interest, advise whether a family member is a government official.",
				"Conflicts of interest include a family member working for a competitor: would you be engaging in any of the above listed situations?"},
		},
		50: {
			Pos: []string{"Do you have a close personal contact who is a director at Example Corp?"},
			Neg: []string{"Do you have a family relationship with a senior government official or employee?", "Do you have a close personal contact in Springfield?",
				"A family relationship with an employee is a conflict: would you be engaging in any of the above listed situations?"},
		},
		57: {
			Pos: []string{"Current company", "Current (or most recent) employer", "Most recent company name"},
			Neg: []string{"Notice period at your current employer", "I understand that my current employer may be contacted.", "Do you have a non-compete with your current employer?",
				"Is your current employer a customer of Example Corp?", "Does your current employer have a business relationship with Example Corp?",
				"What is your job title at your current company?", "What is your current salary at your current employer?", "Reason for leaving current employer", "Current employer's industry"},
		},
		58: {
			Pos: []string{"Current job title", "Present position title", "Current or Most Recent Job Title"},
			Neg: []string{"Current company", "Current job title at your current company"},
		},
		166: {
			Pos: []string{"Can you make a three-year commitment?", "Are you ok with a 2 year commitment?"},
			Neg: []string{"Can you make a long commitment?"},
		},
		167: {
			Pos: []string{"Are you willing to commit to two years?"},
			Neg: []string{"Are you willing to commit?"},
		},
		184: {
			Pos: []string{"What is your earliest start?", "When can you start?", "Whats the earliest you can start?"},
			Neg: []string{"Start date", "What time zone are you in?"},
		},
		185: {
			Pos: []string{"Date available"},
			Neg: []string{"Date of birth"},
		},
		186: {
			Pos: []string{"Start date", "If offered a position, what is your ideal start-date?"},
			Neg: []string{"End date", "End-date"},
		},
		187: {
			Pos: []string{"How soon could you start?", "When can you begin?", "When are you looking to start?", "When is the earliest you would want to start working with us?",
				"When is the earliest date you could begin?"},
			Neg: []string{"How soon is the deadline?", "When are you looking to relocate?", "When is the earliest you would want to relocate?"},
		},
		188: {
			Pos: []string{"Are you available to start immediately?", "Are you able to begin working in two weeks?"},
			Neg: []string{"Are you available on weekends?"},
		},
		189: {
			Pos: []string{"If offered, when would you be able to start?", "If offered the job, how soon are you able to join?"},
			Neg: []string{"If offered, would you accept?"},
		},
		190: {
			Pos: []string{"Available to start", "Earliest possible start date", "Availability date"},
			Neg: []string{"Available to start after the offer", "Available to start at your employer", "Availability to interview"},
		},
		214: {
			Pos: []string{"Are you available full-time?", "Are you available to work 40 hours?"},
			Neg: []string{"Are you available part-time?"},
		},
		223: {
			Pos: []string{"Does the salary expectation align with your expectations?"},
			Neg: []string{"What is your salary expectation?"},
		},
		225: {
			Pos: []string{"Is the posted pay within your expectations?", "Is this salary within your range?"},
			Neg: []string{"Is the office within your range?"},
		},
		359: {
			Pos: []string{"Have you applied to Example Corp on a previous occasion for employment?"},
			Neg: []string{"Have you applied for a loan on a previous occasion?"},
		},
		360: {
			Pos: []string{"In the past, have you applied for employment with us?"},
			Neg: []string{"In the past, have you volunteered here?", "Before we proceed with your application, we would like to ensure that you are comfortable with us processing your personal information for this application and potential employment."},
		},
		361: {
			Pos: []string{"Have you ever applied here?", "Have you previously applied to this company?"},
			Neg: []string{"Have you applied for a loan?"},
		},
		362: {
			Pos: []string{"Have you applied to Example Corp within the past 6 months?"},
			Neg: []string{"Have you applied to Example Corp?"},
		},
		363: {
			Pos: []string{"Any previous interactions with Example Corp?"},
			Neg: []string{"Previous experience with Example Framework"},
		},
		364: {
			Pos: []string{"Have you ever had any engagements with Example Corp?", "Have you had interactions with our team?"},
			Neg: []string{"Have you ever had any experience with Example Corp?"},
		},
		368: {
			Pos: []string{"Can you work overtime?"},
			Neg: []string{"Can you work weekends?"},
		},
		369: {
			Pos: []string{"Are you willing to work overtime?"},
			Neg: []string{"Are you willing to work weekends?"},
		},
		370: {
			Pos: []string{"Overtime hours may be required."},
			Neg: []string{"Overtime pay rates"},
		},
		393: {
			Pos: []string{"Do you have commitments to another employer?"},
			Neg: []string{"Do you have commitments to attend a wedding?"},
		},
		394: {
			Pos: []string{"Do you have commitments that may interfere with your employment?"},
			Neg: []string{"Do you have commitments next week?"},
		},
		395: {
			Pos: []string{"Do you plan to have other employment while working here?"},
			Neg: []string{"Do you plan to have a vacation?"},
		},
		396: {
			Pos: []string{"Describe any employment or other work that you intend to continue."},
			Neg: []string{"Describe any hobby that you intend to continue."},
		},
		408: {
			Pos: []string{"Do you have the ability to influence decisions of the employer?"},
			Neg: []string{"Do you have the ability to lead decisions?"},
		},
		426: {
			Pos: []string{"Have you worked for a firm that serves as the external auditor of Example Corp?"},
			Neg: []string{"Have you worked for an auditor?"},
		},
		427: {
			Pos: []string{"Did you work for the external auditor of Example Corp within the last 12 months?"},
			Neg: []string{"Did you work for the auditor of Example Corp?"},
		},
		434: {
			Pos: []string{"Have you worked for " + rxbAuditFirm + "?"},
			Neg: []string{"Have you worked for Example Corp?"},
		},
		435: {
			Pos: []string{"Did you work for the company's external auditor?"},
			Neg: []string{"Did you work for the company?"},
		},
		436: {
			Pos: []string{"Are you a military spouse?"},
			Neg: []string{"Are you a military veteran?"},
		},
		437: {
			Pos: []string{"Are you a current or former military domestic partner?"},
			Neg: []string{"Are you a civilian domestic partner?"},
		},
		438: {
			Pos: []string{"Is your spouse currently serving in the armed forces?"},
			Neg: []string{"Is your spouse a teacher?"},
		},
		446: {
			Pos: []string{"Have you ever held a security clearance?"},
			Neg: []string{"Do you currently hold a clearance?"},
		},
		447: {
			Pos: []string{"Has any agency investigated your background?"},
			Neg: []string{"Has any agency investigated your neighbor?"},
		},
		448: {
			Pos: []string{"Have you ever filed a security incident report?"},
			Neg: []string{"Have you filed an incident report?"},
		},
	}
}

func rxbCasesEducation() map[int]ruleCases {
	return map[int]ruleCases{
		173: {
			Pos: []string{"Which university did you graduate from?", "What school did you attend?", "Where did you go to University?"},
			Neg: []string{"Did you graduate?", "Which university is your favorite?", "Which university did you attend for your undergraduate degree?", "Where did you go to high school?", "Where did you go to university for your undergraduate degree?"},
		},
		174: {
			Pos: []string{"Did you graduate?"},
			Neg: []string{"When will you graduate?"},
		},
		205: {
			Pos: []string{"GPA", "Latest GPA score", "Grade point average"},
			Neg: []string{"Grade level"},
		},
		310: {
			Pos: []string{"Most recent school", "School you most recently attended"},
			Neg: []string{"School"},
		},
		311: {
			Pos: []string{"University", "School *", "Name of your university", "University/College name", "College or University",
				"Please select your college or University:", "University or School Attended", "School attended"},
			Neg: []string{"Are you currently enrolled in a college/university program?", "School district", "High school attended"},
		},
		312: {
			Pos: []string{"If you don't see your university listed, select Other and type it."},
			Neg: []string{"If your university offers a program, apply now."},
		},
		318: {
			Pos: []string{"Degree focus"},
			Neg: []string{"Degree level"},
		},
		319: {
			Pos: []string{"Are you currently a student?", "Student enrolled in a degree program", "Are you currently enrolled in Full time Education? Like a Bachelor's, Master's, or PhD program",
				"Are you currently in full-time education?"},
			Neg: []string{"Are you a graduate?", "Are you currently enrolled in our employee benefits plan?", "Are you currently in part-time education?",
				"Are you currently in school and willing to work hybrid?"},
		},
		321: {
			Pos: []string{"Month and year of graduation"},
			Neg: []string{"Graduation year"},
		},
		332: {
			Pos: []string{"Expected graduation date"},
			Neg: []string{"Expected graduation year"},
		},
	}
}

// rxbAuditFirm is the audit-firm token the audit-firm-employment route names; it is
// assembled here so the literal appears once.
const rxbAuditFirm = "KP" + "MG"

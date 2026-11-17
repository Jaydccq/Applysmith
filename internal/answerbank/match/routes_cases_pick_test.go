package match

// routeCasesPick holds the table cases of the pick-option family, keyed by
// route priority. Every text is fictional.
func routeCasesPick() map[int]ruleCases {
	return map[int]ruleCases{
		0: {
			Pos: []string{"Please confirm your current nonimmigrant status", "Select your current non-immigrant status"},
			Neg: []string{"Do you hold an immigrant visa?", "What is your current employer?"},
		},
		14: {
			Pos: []string{"Country of citizenship", "In which country do you hold citizenship?", "What is your primary nationality?", "Nationality *",
				"we need you to copy and paste in the name of the country(ies) for which you are a citizen/permanent resident", "Which country are you a citizen of?"},
			Neg: []string{"Second nationality", "Which member countries of the union do you work in?",
				"Are you a citizen/permanent resident of one of the countries listed below?"},
		},
		16: {
			Pos: []string{"List all countries outside the United States where you have lived", "Name all the countries outside your home region"},
			Neg: []string{"Which countries do you want to work in?"},
		},
		18: {
			Pos: []string{"What is your age?", "Age range", "Current age"},
			Neg: []string{"Are you over the age of 18?", "What is your graduation year?"},
		},
		22: {
			Pos: []string{"Who is submitting this application?", "Is this application being completed by a person?", "Are you a real human completing this form?"},
			Neg: []string{"Who is your referrer for this application?", "Are you a student?"},
		},
		23: {
			Pos: []string{"Which cloud platform do you use most?", "What cloud provider are you most familiar with?"},
			Neg: []string{"Do you have experience with cloud platforms?", "Which database do you use?"},
		},
		26: {
			Pos: []string{"Are you willing to travel to our office for an in-person interview?", "Can you fly to Springfield for a final-round interview?"},
			Neg: []string{"Are you willing to travel 25% of the time?", "Can you travel to our office on a weekly basis?"},
		},
		27: {
			Pos: []string{"For the final interview, are you able to travel to Springfield?", "Is the interview one where you must fly to Springfield?",
				"This role may require an in-person interview. Please confirm you would be to travel if needed."},
			Neg: []string{"Are you willing to travel for work?", "Describe an interview you gave",
				"This role requires an in-person interview. Please confirm your availability."},
		},
		56: {
			Pos: []string{"Position function", "Functional area"},
			Neg: []string{"Describe the function of a hash map", "Company industry"},
		},
		62: {
			Pos: []string{"Are you authorized to work in the U.S. and will you require sponsorship?", "Authorization to work: do you need sponsorship now or in the future?"},
			Neg: []string{"Are you authorized to work in the United States?", "Will you require visa sponsorship?"},
		},
		63: {
			Pos: []string{"Sponsorship Requirement", "Sponsorship requirements *"},
			Neg: []string{"Do you have a sponsorship requirement for this role?", "Sponsorship"},
		},
		66: {
			Pos: []string{"If you are under the age of 18, do you have a work permit?", "Under 18: please confirm you hold a work permit"},
			Neg: []string{"Are you over 18?", "Do you hold a work permit?"},
		},
		94: {
			Pos: []string{"Immigration status", "Current immigration status"},
			Neg: []string{"Do you hold an immigrant visa?", "Describe your visa",
				"Do you now, or will you in the future, require Verana Health sponsorship for employment-based immigration status?"},
		},
		95: {
			Pos: []string{"Current visa status", "If you answered no to the above question, what type of visa are you on:", "What kind of visa do you currently hold?"},
			Neg: []string{"Visa type", "What is your current status?", "What type of visa will you require?"},
		},
		96: {
			Pos: []string{"Citizenship Status"},
			Neg: []string{"Country of birth", "What is your citizenship?"},
		},
		97: {
			Pos: []string{"Please describe your current visa status", "Specify visa status",
				"If you require employer sponsorship (H-1B, etc.) to legally work in the U.S., please explain your current work authorization status",
				// Peerspace (Lever, live): the work status in the U.S. is the authorization.
				"Please let us know your work status in the U.S.", "What is your work status in the United States?"},
			Neg: []string{"Describe the sponsorship you need", "Please provide your passport number", "Explain how you got authorization to work on this project",
				// A work status with no country may be employment.
				"What is your current work status?"},
		},
		98: {
			Pos: []string{"Current visa requirements", "Future visa requirement", "Current or future visa requirements"},
			Neg: []string{"What are the visa requirements for your country?", "Visa form"},
		},
		99: {
			Pos: []string{"Enter your visa details for sponsorship", "Enter current visa type, for example H-1B or TN"},
			Neg: []string{"Enter your visa expiry date", "Provide your visa type for sponsorship"},
		},
		103: {
			Pos: []string{"Do you have a bachelor's degree in Computer Science?", "Do you hold a degree in a closely related quantitative field?", "Master's in data science",
				"Do you have a Bachelor's degree in Computer Science or a related field?"},
			// A related field is related to the fields named: beside nursing it
			// is no STEM field (owner workspace, non-software postings).
			Neg: []string{"Do you have experience in computer science?", "Do you have a degree in marketing?",
				"Do you have a Bachelor's degree in Nursing or a related field?", "Do you have a degree in Marketing, Communications, or a related field?",
				"Do you have a Bachelor's degree in Early Childhood Education or a related field?"},
		},
		104: {
			Pos: []string{"Are you majoring in a STEM field?", "Majored in computer science or statistics"},
			Neg: []string{"What is your major?", "Are you majoring in marketing?"},
		},
		322: {
			Pos: []string{"Do you have a master's degree in statistics?", "Do you have a PhD with a focus on mathematics?"},
			Neg: []string{"Do you have a bachelor's degree in history?", "Do you have experience with statistics?"},
		},
		371: {
			Pos: []string{"Which best describes your work authorization?", "Select the option for your work authorization"},
			Neg: []string{"What is your work authorization?", "Which of the following languages do you speak?"},
		},
		372: {
			Pos: []string{"US work authorization", "United States work authorization status"},
			Neg: []string{"Are you authorized to work in the U.S.? Provide US work authorization details", "Work authorization"},
		},
		373: {
			Pos: []string{"What is your current work authorization?", "What is your work authorization",
				// Momentic (Ashby, live).
				"What is your current legal status to work in the USA"},
			Neg: []string{"Which best describes your work authorization?", "What is your current employer", "What is your current legal status in the case?"},
		},
		116: {
			Pos: []string{"Are you based in the US or Canada?", "Are you based out of the United States or Canada"},
			Neg: []string{"Are you currently living in the US or Canada?", "Are you based in Canada or Mexico?"},
		},
		120: {
			Pos: []string{"Which of the following office locations are you willing to relocate to?", "Select all locations you are open to relocating to",
				// Lightmatter (Greenhouse): a checkbox group of its offices.
				"Select the location you can commute or relocate to",
				// Footprint (Ashby, queue173): the offices after the attendance.
				"This role is in-office 5 days a week. Which office could you commit to working from? Check all that apply."},
			Neg: []string{"Are you willing to relocate to one of our office locations if needed?", "Which locations do you like?", "Could you commit to working from our office 5 days a week?", "Can you commit to being in office Monday - Friday? This role is based out of our headquarters in McLean, VA or one of our hubs in New York City or Seattle. If you're not currently located in one of these areas, your willingness to relocate is needed for employment."},
		},
		134: {
			Pos: []string{"Preferred work location", "Preferred locations"},
			Neg: []string{"Location", "Preferred name", "Are you legally authorized to work in your preferred location?"},
		},
		135: {
			Pos: []string{"Which office would you prefer?", "Which location do you prefer to work from?"},
			Neg: []string{"Which office did you visit?", "What do you prefer?"},
		},
		136: {
			Pos: []string{"Location preference", "Location preferences"},
			Neg: []string{"Preferred work arrangement", "Current location"},
		},
		137: {
			Pos: []string{"Location", "Work location *", "Office Location"},
			Neg: []string{"Location of your last employer", "Current location of residence"},
		},
		138: {
			Pos: []string{"Desired work location", "Wanted office locations"},
			Neg: []string{"Desired salary", "Where is your current location"},
		},
		139: {
			Pos: []string{"Which office locations are you open to work from?", "Office location(s) you are willing to work in"},
			Neg: []string{"Office locations are listed on our site", "Are you open to remote work?"},
		},
		140: {
			Pos: []string{"Select office location", "Select your office locations"},
			Neg: []string{"Select your home address", "Select a time"},
		},
		141: {
			Pos: []string{"How many days per week can you be onsite?", "How many days a week would you be in the office?", "How many days per week are you able to work at our office?"},
			Neg: []string{"How many days of vacation do you want per year?", "How many days per week do you work remotely?"},
		},
		191: {
			Pos: []string{"When are you available to start if we make an offer?", "How soon could you be available after an offer"},
			Neg: []string{"Are you available on weekends?", "When can we reach you?"},
		},
		192: {
			Pos: []string{"Would you be available if an offer was accepted?", "Availability after an offer is accepted"},
			Neg: []string{"Are you available for a call?", "Would you accept an offer?"},
		},
		564: {
			Pos: []string{
				"From the point of an offer, how much time would you need before you’re able to start?",
				"How long after receiving an offer would you need to start?",
				"How many weeks would you need to begin once you accept an offer?",
			},
			Neg: []string{"How much time would you need before you're able to start?", "If offered, when could you start?", "How long did you spend on your last offer negotiation?"},
		},
		193: {
			Pos: []string{"Availability", "What is your notice period?", "What is your availability to start?"},
			Neg: []string{"Are you available to work weekends?", "When can you start?", "What is your availability for an interview next week?",
				"Interview availability", "Please state your availability for weekend shifts",
				// Automox (Greenhouse, live): the system's availability.
				"How many years of experience do you have designing, developing, testing, and monitoring applications deployed in a high availability SaaS environment?",
				"Experience with availability zones"},
		},
		639: {
			Pos: []string{"If you will relocate, how much time would you need? If currently based in NYC, please write N/A"},
			Neg: []string{"How long would you need relocation assistance?", "How long have you been in your current role?"},
		},
		208: {
			Pos: []string{"Our COVID-19 vaccination requirement applies to all staff", "Please acknowledge the vaccination policy", "Are you fully vaccinated?"},
			Neg: []string{"Do you have a driver license?", "Describe a policy you wrote"},
		},
		212: {
			Pos: []string{"What is your preferred programming language?", "Preferred coding language for the interview", "Programming language preference"},
			Neg: []string{"Preferred programming language for written and verbal communication", "What is your preferred language of communication?"},
		},
		213: {
			Pos: []string{"When are you available to work full-time?", "How soon are you available for a full time role?"},
			Neg: []string{"Are you available to work full-time?", "When can you start?"},
		},
		215: {
			Pos: []string{"Desired type of employment", "Employment type", "Type of employment"},
			Neg: []string{"Are you available full-time?", "Current employer"},
		},
		250: {
			Pos: []string{"How did you hear about this role? Please specify", "How were you referred? Describe the source"},
			Neg: []string{"How did you hear about this role?", "Please specify your reason",
				"How did you hear about this position? For example: Indeed, LinkedIn, recruiter outreach or other (please describe fully)."},
		},
		251: {
			Pos: []string{"How were you referred to this job?", "How did you hear about this position?", "How did you first hear about us?"},
			Neg: []string{"Were you referred by an employee?", "How did you find the process?"},
		},
		252: {
			Pos: []string{"How did you find out about this opening?", "How did you learn about us?", "How did you first learn about Affirm as an employer?"},
			Neg: []string{"How did you hear about us?", "How did you apply?"},
		},
		253: {
			Pos: []string{"Referral source", "Source of application type"},
			Neg: []string{"Referral name", "Source of funds"},
		},
		254: {
			Pos: []string{"How did you come across this job?", "How did you come to know about us?"},
			Neg: []string{"How did you hear about this job?", "How did you get here?"},
		},
		340: {
			Pos: []string{"How did you learn about this job?", "How did you hear of the opening?"},
			Neg: []string{"How did you learn to code?", "How did you hear about us?"},
		},
		341: {
			Pos: []string{"How did you become aware of this opportunity?", "How did you become aware about us?",
				"How did you discover Infisical? Many engineers discover Infisical through our open source GitHub repo ( https://github.com/Infisical/infisical ) — it's also a great way to get a sense of what we're building."},
			Neg: []string{"Are you aware of the policy?", "How did you hear about this job?"},
		},
		567: {
			Pos: []string{"What source(s) of information influenced your decision to apply for a P&G position?", "Which sources of information led you to apply?"},
			Neg: []string{"What source of information do you use for news?", "Please provide your sources of information."},
		},
		342: {
			// Allium (Ashby, live): a referrer or, with none, how the applicant found the company.
			Pos: []string{"How did you hear about us?", "Who referred you to Allium, or how did you find out about us?"},
			Neg: []string{"How did you hear about this job?", "What do you hear from us?"},
		},
		343: {
			Pos: []string{"Where did you first see this job?", "Where did you hear about us?", "What brought you to this job posting", "Where have you seen Qualtrics prior to applying? Select all that apply.", "Where have you heard of us?",
				"Who referred you or where did you see our job ad?",
				// SentiLink (Ashby, queue178).
				"Where did you learn about this job?"},
			Neg: []string{"Where do you see yourself in five years?", "Where did you go to school?", "What brought you to this role?", "Where have you seen yourself growing?", "Where have you worked before?",
				"Where did you learn to code?"},
		},
		260: {
			Pos: []string{"How familiar are you with AI tools?", "Familiarity with AI tooling"},
			Neg: []string{"Are you comfortable using AI tools?", "How familiar are you with spreadsheets?"},
		},
		265: {
			Pos: []string{"Regarding future positions, may we contact you?", "Communications regarding future opportunities"},
			Neg: []string{"Regarding this position, what is your salary?", "Future plans"},
		},
		266: {
			Pos: []string{"I would like to receive communications about future roles", "Receive communication regarding future jobs"},
			Neg: []string{"Receive communications about this application", "Future communications"},
		},
		272: {
			Pos: []string{"I accept the terms and conditions", "Terms & Conditions"},
			Neg: []string{"I accept the privacy policy", "Conditions of employment"},
		},
		273: {
			Pos: []string{"This assessment is proctored", "Do you consent to proctoring?"},
			Neg: []string{"Do you consent to a background check?", "Is the assessment timed?"},
		},
		274: {
			Pos: []string{"Please confirm and agree to the following rules for the assessment", "Confirm and agree: the assessment is monitored"},
			Neg: []string{"Please confirm and agree to the following rules", "Confirm your assessment"},
		},
		283: {
			Pos: []string{"Years of relevant professional experience", "Years of relevant professional work experience"},
			Neg: []string{"Years of experience", "Relevant professional certifications"},
		},
		293: {
			Pos: []string{"Pronouns", "What are your pronouns (optional)?"},
			Neg: []string{"Preferred name", "What is your gender?"},
		},
		294: {
			Pos: []string{"Gender", "Gender identity", "How would you describe your gender?"},
			Neg: []string{"Are you transgender?", "What is your race?"},
		},
		295: {
			Pos: []string{"Race", "Racial/ethnic background", "Please select your races"},
			Neg: []string{"Are you Hispanic or Latino? (regardless of race)", "Ethnicity"},
		},
		296: {
			Pos: []string{"Ethnicity", "Hispanic/Latino", "Are you Hispanic?", "Ethnic background"},
			Neg: []string{"Race", "Do you speak Spanish?"},
		},
		297: {
			Pos: []string{"Veteran status", "Are you a protected veteran?"},
			Neg: []string{"Are you a military spouse?", "Do you hold a security clearance?"},
		},
		298: {
			Pos: []string{"Disability status", "Voluntary disability status"},
			Neg: []string{"Disabled veteran", "Employment status"},
		},
		299: {
			Pos: []string{"Yes, I have a disability", "I have had a disability in the past"},
			Neg: []string{"I have a car", "Yes, I have a degree"},
		},
		300: {
			Pos: []string{"Please self-identify whether you have a disability", "Please self-identify if you have a disability"},
			Neg: []string{"Please self-identify your gender", "Disability"},
		},
		301: {
			Pos: []string{"Disability: please self-identify below", "Disabilities - self-disclose if applicable"},
			Neg: []string{"Disability benefits are offered", "Your status"},
		},
		302: {
			Pos: []string{"Do you have a disability?", "Do you currently have a disability or chronic condition?", "Are you a person with a disability?"},
			Neg: []string{"Do you have a car?", "Have you served in the military?"},
		},
		304: {
			Pos: []string{"Do you consider yourself to have a disability or long-term health condition?"},
			Neg: []string{"Do you consider yourself to have a disability or long-term health condition that needs reasonable accommodation?", "Do you consider yourself an expert?"},
		},
		305: {
			Pos: []string{"I have a disability", "I do not have a disability", "I don't have a disability"},
			Neg: []string{"I have a disability and need assistance", "I have a degree"},
		},
		306: {
			Pos: []string{"Disability", "Disability *"},
			Neg: []string{"Disability accommodations: do you require any?", "Do you have a disability"},
		},
		307: {
			Pos: []string{"Do you identify as LGBTQ+?", "Do you consider yourself LGBTQIA?"},
			Neg: []string{"LGBTQ+", "Sexual orientation"},
		},
		308: {
			Pos: []string{"Sexual orientation", "LGBTQ+", "Are you part of the LGBT community?"},
			Neg: []string{"Gender identity", "What is your orientation date?"},
		},
		313: {
			Pos: []string{"If you don't see your major listed, please enter it here", "If your field of study is not listed, type it below"},
			Neg: []string{"What was your major?", "If you do not see your phone listed, call us"},
		},
		314: {
			Pos: []string{"Current degree year", "Which year of your degree are you in?", "What year of study are you in?"},
			Neg: []string{"Degree", "What year did you graduate?"},
		},
		315: {
			Pos: []string{"Level of education currently pursuing", "Current level of education you are pursuing"},
			Neg: []string{"Highest level of education", "Level of experience"},
		},
		316: {
			Pos: []string{"Current level of education"},
			Neg: []string{"Highest level of education", "Level of education"},
		},
		317: {
			Pos: []string{"Undergraduate field of study", "Bachelor's major", "Undergrad area of study"},
			Neg: []string{"Field of study", "Graduate major"},
		},
		320: {
			Pos: []string{"Field of study", "Area of study", "What is your major?", "Discipline"},
			Neg: []string{"Do you have a disability that affects a major life activity?", "Have you had a major illness or major surgery?", "Do you have a disability? Field of study"},
		},
		323: {
			Pos: []string{"Degree", "Degree *"},
			Neg: []string{"Degree earned", "Degree type or level"},
		},
		324: {
			Pos: []string{"Highest level of education", "Level of education", "What is your highest level of education?"},
			Neg: []string{"Level of experience", "Education history"},
		},
		325: {
			Pos: []string{"Highest education level", "What is your highest education level?"},
			Neg: []string{"Education level desired", "Highest degree"},
		},
		326: {
			Pos: []string{"Highest degree", "What is your highest degree?"},
			Neg: []string{"Degree", "Highest salary"},
		},
		327: {
			Pos: []string{"Degree attained", "Degree earned", "Degree completed"},
			Neg: []string{"Do you have a degree?", "Degrees of freedom"},
		},
		328: {
			Pos: []string{"Level of degree"},
			Neg: []string{"Degree level", "Level of education"},
		},
		329: {
			Pos: []string{"Expected graduation month", "Actual graduation month"},
			Neg: []string{"Expected graduation year", "Graduation date"},
		},
		330: {
			Pos: []string{"Graduation year", "Expected graduation year"},
			Neg: []string{"Expected graduation month", "Year of birth"},
		},
		331: {
			Pos: []string{"When will you graduate?", "When do you expect to graduate?", "When are you graduating?"},
			Neg: []string{"Have you graduated?", "When will you start?"},
		},
		334: {
			Pos: []string{"Expected graduation", "Anticipated graduation term"},
			Neg: []string{"Expected graduation year", "Anticipated graduation month"},
		},
		571: {
			Pos: []string{"SAT / ACT / GRE Score", "SAT or ACT score"},
			Neg: []string{"SAT Score", "GRE Score", "Act on feedback"},
		},
		417: {
			Pos: []string{"What is your military status?", "Your military status"},
			Neg: []string{"Military branch", "Marital status"},
		},
		439: {
			Pos: []string{"Military branch", "Branch of the military", "Branch of service"},
			Neg: []string{"Military status", "Branch office location"},
		},
		440: {
			Pos: []string{"Have you served in the National Guard?", "Are you a member of a reserve component?", "Did you serve in the U.S. military?"},
			Neg: []string{"Is your spouse serving in the armed forces?", "Have you served on a jury?"},
		},
		441: {
			Pos: []string{"Have you served in the U.S. Armed Forces?", "Are you currently serving in the military service?"},
			Neg: []string{"Is your spouse a member who served in the armed forces?", "Do you have customer service experience?"},
		},
		442: {
			Pos: []string{"Are you a US citizen with the ability to obtain and maintain a security clearance?", "Do you hold a clearance? Are you a United States citizen?",
				"This position requires eligibility to obtain and maintain a U.S. security clearance.", "Are you eligible for a security clearance?"},
			Neg: []string{"Do you hold a clearance?", "Are you a US citizen?", "Are you eligible to work in the United States?", "Are you eligible for a sign-on bonus?"},
		},
		443: {
			Pos: []string{"Are you willing to obtain a security clearance?", "Are you able to obtain and maintain a top secret clearance?"},
			Neg: []string{"Are you a U.S. citizen willing to obtain a security clearance?", "Are you willing to relocate?"},
		},
		444: {
			Pos: []string{"Is your clearance currently active?", "Clearance active"},
			Neg: []string{"Do you hold an active security clearance?", "Is your clearance active? Are you a US citizen?"},
		},
		445: {
			Pos: []string{"From which agency was it issued?", "From which federal agency did you obtain it?"},
			Neg: []string{"Which recruitment agency referred you?", "From which school did you graduate?"},
		},
		449: {
			Pos: []string{"Do you hold an active security clearance?", "Security clearance", "Security Clearances"},
			Neg: []string{"Do you hold a clearance?", "Security training"},
		},
		450: {
			Pos: []string{"Government clearance", "Government security clearance"},
			Neg: []string{"Government experience", "Clearance level"},
		},
		451: {
			Pos: []string{"Clearance level", "Clearance status", "Clearance type"},
			Neg: []string{"Security level", "Clearance sale"},
		},
		452: {
			Pos: []string{"Level of clearance", "What clearance do you hold?", "Do you hold a clearance?"},
			Neg: []string{"Level of education", "Do you hold a license?", "Clearance sale"},
		},
		453: {
			Pos: []string{"Are you a U.S. citizen?", "US citizenship", "Are you a United States citizen", "Are you a citizen of the U.S.?"},
			Neg: []string{"Are you a citizen of Canada?", "Is this a customs citizen program"},
		},
		476: {
			Pos: []string{"Do you wish to opt out of AI screening?", "Opt-out of automated decision making", "Artificial intelligence tools may be used; you can opt out"},
			Neg: []string{"Do you wish to opt out of emails?", "We use AI to help screen. Do you consent?"},
		},
		477: {
			Pos: []string{"We use an automated employment decision tool", "Automated employment decision tools notice"},
			Neg: []string{"We use automated email", "Employment decision"},
		},
		478: {
			Pos: []string{"AEDT consent", "Acknowledge the AEDT notice"},
			Neg: []string{"Accept the notice", "Mandated audit"},
		},
		488: {
			Pos: []string{"Interview recording: I consent", "This interview is recorded; please confirm you agree", "Interview Recording and AI Notetaker — Notice", "We may use an AI notetaker during your interview",
				// Block (Greenhouse, live): AI transcription and summary; the owner's decision answers Yes.
				"Do you consent to us using AI to transcribe and summarize your interview?",
				// Palantir (Lever, live).
				"As part of our interview process, we may use AI notetakers to transcribe conversations for accuracy and efficiency. Please see our candidate privacy policy for more information on how we process your data. Your decision to opt in or out of this tooling will not impact your candidacy.",
				"We use an AI notetaking tool to summarize interviews."},
			Neg: []string{"Please confirm your email address", "Consent to the privacy policy", "What is your availability to interview?", "Note taking software you use", "Summarize your interview experience with us"},
		},
		489: {
			Pos: []string{"I consent to the interview being recorded", "Please confirm the interview will be recorded", "Confirm that you agree to the interview recording"},
			Neg: []string{"I consent to a background check", "Recording artist"},
		},
		501: {
			Pos: []string{"Salutation", "Courtesy title", "Name prefix", "Honorific"},
			Neg: []string{"Job title", "Preferred name"},
		},
		507: {
			Pos: []string{"Which country are you located in?", "Country/Region of residence: where do you live", "In which country do you currently reside?"},
			Neg: []string{"Are you authorized to work in the country in which this job is based?", "Country phone code where you are located",
				"Do you currently have legal work authorization in the country where this position is based?"},
		},
		512: {
			Pos: []string{"Do you live in any of the following states?", "Do you currently reside in the following states"},
			Neg: []string{"What state do you live in?", "Do you live in a rural area?"},
		},
		513: {
			Pos: []string{"State", "State/Province", "Province", "Region", "Which state do you live in?"},
			Neg: []string{"State your reason for applying", "Base salary requirements (do not state negotiable)", "Describe how you handle application state management",
				"Because Example Corp manages assets for state and local governments, have you made a political contribution in the last two years?",
				"Are you a relative of a senior level person for a company other than State Street?",
				"Are you currently admitted to and in good standing with at least one US state or federal bar?",
				"Have you worked for a state-owned enterprise?",
				"In which state are you registered with FINRA?", "State of licensure", "Which state issued your nursing license?"},
		},
		515: {
			Pos: []string{"Country", "Country of residence", "Nation"},
			Neg: []string{"Country of citizenship", "Country code for your phone", "Which country issued your licence?",
				"Please select the country or countries you anticipate working in for the role in which you are applying."},
		},
		517: {
			Pos: []string{"Time zone", "Timezone", "Please select the option(s) that best describes your location and schedule flexibility."},
			Neg: []string{"Time commitment", "Zone", "Please describe your schedule flexibility."},
		},
		520: {
			Pos: []string{"Phone type", "Phone device type"},
			Neg: []string{"Phone number", "Device model"},
		},
		521: {
			Pos: []string{"Device type", "Type of phone"},
			Neg: []string{"Phone number", "Type of employment"},
		},
	}
}

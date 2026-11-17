package match

import "strings"

func routeCasesOther() map[int]ruleCases {
	return mergeRuleCases(othCasesAcknowledge(), othCasesCompensation(), othCasesEmployer(), othCasesLanguageAndMulti())
}

func othCasesAcknowledge() map[int]ruleCases {
	longPrompt := strings.Repeat("Our matching tool reads your answers and ranks roles for you. ", 7) +
		"See the privacy notice for details. Opt In or Opt Out."
	longLabel := "Employee Handbook And Remote Work Policy For Example Corp Staff Worldwide"
	return map[int]ruleCases{
		270: {
			Pos: []string{
				"Please review and acknowledge the Job Applicant Privacy Notice",
				"I consent to the data processing described in the Example Corp privacy policy",
				"Privacy Statement: I agree to the collection of my data",
			},
			Neg: []string{
				"Which team's privacy tooling have you worked on?",
				"Do you agree to relocate to Springfield?",
			},
		},
		271: {
			Pos: []string{
				"Applicant Privacy Notice",
				"Example Corp Data Protection Notice *",
			},
			Neg: []string{
				"Do you accept the privacy policy",
				"Privacy Policy - opt in to updates",
				longPrompt,
				"What is the privacy policy?",
			},
		},
		275: {
			Pos: []string{
				"By submitting this application, you affirm that the information provided above is true, accurate and complete",
				"I certify that my statements are true",
			},
			Neg: []string{
				"Which certifications do you hold?",
				"Please confirm your preferred start date",
			},
		},
		586: {
			Pos: []string{
				"Please double-check all the information provided above. Ensuring accuracy is crucial, as any errors or omissions may impact the review of your application.",
				"Please review the information you have entered and make sure it is accurate",
			},
			Neg: []string{
				"Please review the information below about our benefits.",
				"Please verify your email address",
				"Have you reviewed the job description above?",
			},
		},
		632: {
			Pos: []string{
				"If any information I have provided is untrue, or if I have concealed material information, I understand that this will constitute cause for the denial of employment or immediate dismissal.",
				"I understand that if any statements I have made are false, it may result in rejection of my application or termination.",
			},
			Neg: []string{
				"Have you ever been dismissed from a job?",
				"If any information is incomplete, please explain below.",
			},
		},
		276: {
			Pos: []string{
				"I acknowledge that I have read the policy and answered the above questions truthfully and accurately",
				"Answers must be correct. I hereby acknowledge that all entries are accurate",
				"fraud is discoveredI acknowledge the answers are accurate",
			},
			Neg: []string{
				"Please acknowledge that your answers are truthful",
				"Section 2I acknowledge the answers are accurate",
				"I acknowledge the terms. The data is accurate",
			},
		},
		494: {
			Pos: []string{"I consent to the use of my information", "Consent"},
			Neg: []string{"Are you a consultant?", "What is your desired salary?"},
		},
		495: {
			Pos: []string{"Are you willing to submit to a background check?", "I agree to sign the offer letter", "Do you agree to notify the university of any future travel?"},
			Neg: []string{"Are you willing to relocate?", "Are you willing to notify your manager?"},
		},
		638: {
			Pos: []string{"If extended an offer of employment, would you be willing to provide professional references?"},
			Neg: []string{"Are you willing to provide proof of eligibility to work?"},
		},
		640: {
			Pos: []string{"How do you rate your coding skills out of 10 without AI assistance?"},
			Neg: []string{"How do you rate your communication skills out of 10?", "Rate your coding skills on a scale of 1 to 5"},
		},
		496: {
			Pos: []string{"I agree to submit this form electronically", "You will submit this application electronically"},
			Neg: []string{"Please submit your resume", "Electronically signed forms are not accepted here"},
		},
		497: {
			Pos: []string{"I agree that my electronic signature is binding"},
			Neg: []string{"Please sign your name below", "Electronic devices are not allowed"},
		},
		498: {
			Pos: []string{
				"By checking this box I accept the terms of use",
				"I certify that I accept the code of conduct",
			},
			Neg: []string{
				"Please indicate by checking this box whether you accept the veteran status description",
				"Describe your hobbies by checking your notes",
			},
		},
		499: {
			Pos: []string{
				"I agree to the code of conduct; by checking this box I continue",
				"You accept the handbook. I acknowledge this.",
			},
			Neg: []string{
				"Self-identify your gender identity: consent to the policy by checking the box",
				"Policy updates are posted monthly",
			},
		},
		554: {
			Pos: []string{
				"I acknowledge the above",
				"We reserve the right to end employment if fraud is discoveredI acknowledge the above",
				"I have read the handbook",
			},
			Neg: []string{
				"Do you agree to relocate to Seattle?",
				"Please confirm your preferred start date",
				"Item 2I agree",
			},
		},
		555: {
			Pos: []string{
				"Kindly ensure your application is complete. Please acknowledge.",
				"Please confirm your acknowledgement",
				"Acknowledge the following statement",
			},
			Neg: []string{
				"We acknowledge receipt of applications",
				"Did AI complete this application?",
			},
		},
		556: {
			Pos: []string{"GDPR", "I accept the California Consumer Privacy Act notice"},
			Neg: []string{"Do you know the general data policy?"},
		},
		557: {
			Pos: []string{
				"AI Policy for Application *",
				"Agreement to Arbitrate",
				"Certification & Attestation",
			},
			Neg: []string{
				"Do you have a company policy",
				"Non-compete agreement",
				longLabel,
				"Where do you live",
			},
		},
	}
}

func othCasesCompensation() map[int]ruleCases {
	return map[int]ruleCases{
		219: {
			Pos: []string{"What are your total cash compensation expectations?", "Total comp target"},
			Neg: []string{"What is your base salary expectation?"},
		},
		220: {
			Pos: []string{"On-target earnings expectation", "Your total cash expectations"},
			Neg: []string{"What are your equity expectations?"},
		},
		221: {
			Pos: []string{
				"Desired total annual compensation amount and currency",
				"Expected annual gross total compensation",
			},
			Neg: []string{"Total compensation currency", "What is your total experience?"},
		},
		222: {
			Pos: []string{"Target yearly total comp", "Desired gross total yearly compensation"},
			Neg: []string{"Salary currency"},
		},
		227: {
			Pos: []string{"Salary expectation in your local currency"},
			Neg: []string{"Salary expectation"},
		},
		228: {
			Pos: []string{"Minimum annual salary requirement", "Max pay expectation"},
			Neg: []string{"Annual salary band"},
		},
		229: {
			Pos: []string{"Base pay requirements", "Compensation expectation (USD)", "Pay expectations", "What are your compensations expectations? Please provide a range."},
			Neg: []string{"What is your pay grade?", "Compensations paid to employees are reviewed every year."},
		},
		231: {
			Pos: []string{"Please indicate the base salary you are seeking", "The pay you are seeking"},
			Neg: []string{
				"Provide details on our benefits packages and perks for all staff. Salary information is posted online.",
				"Please list your skills", "Please provide your current salary.", "Please provide your most recent salary", "Please list your salary history",
			},
		},
		232: {
			Pos: []string{"What is your base pay expectation?"},
			Neg: []string{"What is your base pay rate?"},
		},
		233: {
			Pos: []string{"What are your salary requirements?", "What is your compensation expectation?"},
			Neg: []string{"What are your hobbies?"},
		},
		234: {
			Pos: []string{"What is your desired annual salary?"},
			Neg: []string{"What is your desired start date?"},
		},
		235: {
			Pos: []string{"What is your expected salary?"},
			Neg: []string{"What is your expected graduation date?"},
		},
		236: {
			Pos: []string{"What would you expect to earn per year?"},
			// An ask of a pay per hour is the hourly rate's (hourlyPayOnly).
			Neg: []string{"What do you expect from this role?", "Expecting to be paid hourly or by project"},
		},
		237: {
			Pos: []string{
				"Desired Annual Pay:*",
				"Preferred monthly base compensation",
				"What is your minimum required yearly salary (in USD) to consider this position?",
				"What salary are you targeting?",
				"What is your targeted salary range for your next opportunity?",
				"What is your ideal compensation range?",
				"What is your required base salary per the range listed (and OTE for sales roles)?",
			},
			Neg: []string{"What is your current monthly net salary?", "What is your ideal work environment?", "Is the required salary range acceptable to you?"},
		},
		238: {
			Pos: []string{"What is your minimum base salary?", "What is your ideal salary?"},
			Neg: []string{"What is your current salary?"},
		},
		239: {
			Pos: []string{"Salary Desired *", "Pay Range:"},
			Neg: []string{"Salary desired for this position, mention bonus too", "Pay range?"},
		},
		240: {
			Pos: []string{"Salary requirements"},
			Neg: []string{"Salary history"},
		},
		574: {
			Pos: []string{"Will you require relocation assistance?", "Will you need relocation assistance to work at this role's specified location?", "Will you require a relocation package?", "Are you seeking relocation assistance?", "Would you require relocation support?"},
			Neg: []string{"Will you require relocation?", "Does this role offer relocation assistance?", "Relocation assistance is available for this role. Are you willing to relocate?"},
		},
		572: {
			Pos: []string{
				"Do you have any competing offer deadlines?",
				"New Grad 2027 Application Questions : Do you have any pending offers or deadlines we should know about? If so, what companies and dates?",
				"Are you currently holding any outstanding job offers?",
				"Do you have other offers of employment with deadlines?",
				// Semgrep (Ashby, queue164).
				"Do you have any deadlines we should be aware of?",
			},
			Neg: []string{"Would you like to receive other offers and promotions from us?", "Does the company offer relocation?",
				"How do you handle tight deadlines?", "Can you meet project deadlines?"},
		},
		533: {
			Pos: []string{
				"What is your desired bonus expectation? Please specify the amount and currency.",
				"Target bonus (% of base)",
				"What annual bonus do you expect?",
			},
			Neg: []string{"Are you eligible for a sign-on bonus?", "What is your sign-on bonus expectation?", "Did you receive a bonus last year?"},
		},
	}
}

func othCasesEmployer() map[int]ruleCases {
	return map[int]ruleCases{
		31: {
			Pos: []string{
				"Have you ever worked for Example Corp?",
				"Have you previously worked at Example Corp",
				"Have you in the past worked with Example Corp",
				"Have you previously worked within the Motional ecosphere? (ex. Motional, Ottomatika, Aptiv, nuTonomy, or Hyundai Motor Company)",
			},
			Neg: []string{"Have you worked for Example Corp?", "Have you ever worked with TypeScript?", "Have you previously worked within a regulated industry?"},
		},
		32: {
			Pos: []string{"Have you worked for Example Corp before?"},
			Neg: []string{"Have you worked with TypeScript before?", "Have you worked with these tools before?"},
		},
		33: {
			Pos: []string{"Have you worked for Example Corp in the past?", "Have you worked at Example Corp in the past?*"},
			Neg: []string{
				"Have you worked for Example Corp in the past 5 years?",
				"Have you worked with these tools in the past?",
			},
		},
		34: {
			Pos: []string{
				"Have you ever worked as an intern at Example Corp?",
				"Have you previously worked as a contractor for Example Corp?",
			},
			Neg: []string{"Have you ever worked as a data scientist with Python?"},
		},
		35: {
			Pos: []string{"Have you been employed by Example Corp?", "Have you ever been an employee of Example Bank?",
				"Have you ever been a team member of Example Bank?"},
			Neg: []string{"Have you been to Springfield?", "Have you been referred by a team member of Example Bank?", "Have you been employed by a federal agency?"},
		},
		36: {
			Pos: []string{"Were you ever employed by Example Corp?", "Were you working at Example Corp",
				"Do either of the following statements apply to you? I am currently employed by Ernst & Young at the partner level or above. I am/was employed by Ernst & Young at some point in the last 7 years and directly supported Q2's account.",
				"Does any of the following statements apply to you? I was employed by Example Corp."},
			Neg: []string{"Were you born in Springfield?",
				"Do any of the following statements apply to you? I am employed by Example Corp. My spouse is employed by Example Corp.",
				"Do either of the following statements apply to you? I hold a financial interest in Example Corp.",
				"Do any of the following statements apply to you? I am employed by a federal government agency."},
		},
		37: {
			Pos: []string{
				"Are you a current or former employee of Example Corp or its subsidiaries?",
				"Are you a former Example Corp (including its subsidiaries) employee?",
				// Compeer Financial (Greenhouse, live): "team member" is the employer's word for employee.
				"Are you a previous team member of Example Financial?", "Are you a current team member at Example Financial?",
			},
			Neg: []string{
				"Is any close relative a current or former employee of Example Corp?",
				"How many employees does your team have?",
				"Were you referred by a current team member?", "Do you know a former team member of Example Corp?",
				"Do you have a close personal relationship with a current employee of the company?", "Do you know a current employee at Example Corp?",
				// Babylist (Greenhouse, live): the referral names the employee.
				"Were you referred to this role by a current Example Corp employee?",
				// Weave (Ashby, live): the referral note under "How did you hear about us?" asks no employment.
				"How did you hear about us? If you were introduced to Example Corp by a current employee and would like to be considered a referral, the employee must submit you as a referral.",
			},
		},
		38: {
			Pos: []string{"Previously worked for Example Corp", "Formerly employed at Example Corp"},
			Neg: []string{"Are you employed by another company?"},
		},
		39: {
			Pos: []string{
				"Have you provided services to Example Corp as a contractor?",
				"Were you a vendor that provided services to us?",
			},
			Neg: []string{"Have you previously provided services to clients in the healthcare industry?"},
		},
		40: {
			Pos: []string{"Do you currently work for Example Corp?", "Are you currently employed by Example Corp?"},
			Neg: []string{
				"Do you work for Example Corp?",
				"Are you currently employed by Example Corp? If offered a position, when can you start?",
				"Are you currently working for Example Corp or have you previously worked at Example Corp?",
			},
		},
	}
}

func othCasesLanguageAndMulti() map[int]ruleCases {
	return map[int]ruleCases{
		198: {
			Pos: []string{"Which languages do you speak fluently?"},
			Neg: []string{"Which languages are on your resume?"},
		},
		199: {
			Pos: []string{"OVERALL*", "Overall"},
			Neg: []string{"Overall experience", "Overall rating"},
		},
		200: {
			Pos: []string{"Read *", "Speaking", "Write"},
			Neg: []string{"Read the policy", "Write a short note about yourself"},
		},
		201: {
			Pos: []string{"Language*", "Indicate your proficiency of the English language"},
			Neg: []string{
				"Alternate Language First Name",
				"Location of Residence and Language:",
				"Have you fine-tuned large language models?",
				"Which programming languages do you use?",
				"Do you speak a language other than English?",
				"Do you have at least 2 years of experience in scripting in some language (Python, Bash/Shell, PowerShell, or JavaScript)?",
				"In your most recent role, which backend language did you use most heavily?",
				"What front-end language are you strongest in?",
			},
		},
		202: {
			Pos: []string{"Speaking proficiency", "Fluency level for reading"},
			Neg: []string{"Describe your education level"},
		},
		24: {
			Pos: []string{"Which technologies and tools have you used in production data pipelines? Select up to 3"},
			Neg: []string{"Which cities do you prefer?", "What tools do you use for design?"},
		},
		25: {
			Pos: []string{"Which data platforms have you used in production?", "What databases have you worked with?"},
			Neg: []string{"Which cloud region is closest to you?"},
		},
		428: {
			Pos: []string{"Have you worked at PwC or Deloitte?", "Ernst & Young and KPMG", "PwC or PricewaterhouseCoopers and EY"},
			Neg: []string{"Have you worked at KPMG?", "Worked at KPMG or KPMG?", "EY, that is Ernst & Young"},
		},
		429: {
			Pos: []string{"Big 4 accounting firm: have you ever worked there?", "A Big Four firm where you were employed"},
			Neg: []string{"Do you like accounting?", "Have you worked at a Big 4 firm"},
		},
		430: {
			Pos: []string{"Select the accounting firms you have joined", "Which accounting firm did you join?"},
			Neg: []string{"Which team do you prefer?"},
		},
		431: {
			Pos: []string{"Shift availability"},
			Neg: []string{"Available start date"},
		},
		432: {
			Pos: []string{"Which shifts are you available for?"},
			Neg: []string{"Are you willing to work overtime?"},
		},
		433: {
			Pos: []string{"Please state your availability for weekend shifts"},
			Neg: []string{"Available to start in June"},
		},
	}
}

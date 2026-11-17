package match

func routeCasesFixedAConsent() map[int]ruleCases {
	return map[int]ruleCases{
		169: {
			Pos: []string{"Are you willing to sign onboarding documents electronically?", "Are you able to sign the onboarding documents?"},
			Neg: []string{"Will you sign the NDA?", "Do you have onboarding experience?"},
		},
		170: {
			Pos: []string{"Stay up to date with new roles", "Sign up for job alerts", "Subscribe to our newsletter", "Keep me informed about openings"},
			Neg: []string{"Do you consent to the privacy policy?", "Tell us about your alerts policy"},
		},
		175: {
			Pos: []string{"Is it OK to contact you by phone?", "Is it okay to contact you?"},
			Neg: []string{"Are you ok with travel?", "Who should we contact?"},
		},
		182: {
			Pos: []string{"Do you meet all the minimum requirements?", "Do you meet the following qualifications?", "Do you meet these criteria?"},
			Neg: []string{"Do you meet the dress code?", "Do you have the requirements?"},
		},
		183: {
			Pos: []string{"Do you have the minimum qualifications?", "Do you have at least the basic job requirements?",
				// DNSFilter (Ashby, live).
				"Before applying, please confirm that you have read the entire job description and understand and have experience with the role qualifications and responsibilities needed for this role.",
				"Please confirm you have read the job posting and meet the minimum qualifications."},
			Neg: []string{"Do you have the time?", "Do you have the minimum salary?", "Please confirm that you have read the entire job description.", "Please confirm you have read our privacy notice."},
		},
		195: {
			Pos: []string{"Are you fluent in any language other than English?", "Are you proficient in a language besides English?"},
			Neg: []string{"Are you fluent in English?", "Which language is your first?"},
		},
		196: {
			Pos: []string{"Do you speak any additional languages?", "Do you speak a foreign language?", "Do you speak another language?"},
			Neg: []string{"Do you speak at conferences?", "Do you speak English?"},
		},
		197: {
			Pos: []string{"Are you bilingual?", "Multilingual"},
			Neg: []string{"Are you a lingual specialist?", "Are you available?"},
		},
		565: {
			Pos: []string{"Can you perform all of the essential functions of this role with or without reasonable accommodations?", "Are you able to perform the essential job functions of this position?"},
			Neg: []string{"What reasonable accommodations do you need to perform the essential functions?", "Can you perform under pressure?"},
		},
		206: {
			Pos: []string{"What reasonable accommodations do you need?", "What accommodations will you require?"},
			Neg: []string{"Do you need reasonable accommodation?", "What is your name?"},
		},
		207: {
			Pos: []string{"Do you come from a low income background?", "Low-income status"},
			Neg: []string{"What is your income expectation?", "Are you a high earner?"},
		},
		209: {
			Pos: []string{"Do you require reasonable accommodation to perform the job?", "Will you need accommodations?"},
			Neg: []string{"What accommodations would you like?", "Do you have a job?"},
		},
		210: {
			Pos: []string{"Applicants who require any reasonable accommodations should say so", "Require accommodation"},
			Neg: []string{"Do you provide accommodation?", "We require a resume"},
		},
		211: {
			Pos: []string{"Applicants who need any accommodation should say so", "Need reasonable accommodations"},
			Neg: []string{"We need a resume", "Hotel accommodation"},
		},
		216: {
			Pos: []string{"Preferred method of contact", "Preferred contact preference", "Communication method", "How would you like to be contacted?"},
			Neg: []string{"Communication skills", "What is your phone number?"},
		},
		217: {
			Pos: []string{"What are you looking for in your next role?", "What are you looking for in your next opportunity?"},
			Neg: []string{"What are you looking for in a manager?", "What do you want in your next role?"},
		},
		218: {
			Pos: []string{"What do you want in your next role?", "What do you want in your next position?"},
			Neg: []string{"What do you want for lunch?", "What are you looking for in your next role?"},
		},
		224: {
			Pos: []string{"Are you comfortable with the posted salary range?", "Is the posted salary band aligned with your expectations? Are you aligned with the salary band?", "Do you align with the salary range?",
				"Can you confirm you've reviewed the compensation band and are comfortable proceeding?"},
			Neg: []string{"What is your salary range?", "Are you comfortable with relocation?", "What salary range are you comfortable with?", "Which pay band are you comfortable with?"},
		},
		226: {
			Pos: []string{"In which country do you anticipate working?", "Which country will you be working in?", "What countries do you plan on working from?"},
			Neg: []string{"Which country do you plan on working in, and are you authorized to work there?", "Country of citizenship", "Which country will you be working in, and do you need a visa?"},
		},
		241: {
			Pos: []string{"Is that level of compensation acceptable for you?", "Does the salary work for you?", "Is the pay agreeable?", "Is this range acceptable? It covers pay"},
			Neg: []string{"Min. Acceptable Monthly Salary", "Expected compensation", "Is that acceptable for you?", "Acceptable salary"},
		},
		242: {
			Pos: []string{"Referred By", "Referred by *", "Referred By:"},
			Neg: []string{"Referred by a current employee?", "If referred by a current employee, please provide the name"},
		},
		243: {
			Pos: []string{"Referrer name", "Referrer's name"},
			Neg: []string{"Referrer email", "Name"},
		},
		244: {
			Pos: []string{"Name of the referrer", "Name of referrer", "Who referred you to Allium?",
				"If you were referred by a Current Employee, please list their name. If this is not applicable, please list N/A.",
				// GHX (Greenhouse, live).
				"If you were referred to this position by a current GHX employee, or other individual, please provide the person’s name."},
			Neg: []string{"Name of the manager", "Referrer email", "Who referred you to Allium, or how did you find out about us?",
				"Who referred you or where did you see our job ad?"},
		},
		246: {
			Pos: []string{"Were you referred by someone?", "Are you being referred through the Campus Referral Program?"},
			Neg: []string{"Are you available?", "Have you been to Springfield?"},
		},
		247: {
			Pos: []string{"Have you been referred by an employee?", "Did you received a referral?",
				// Leap (Ashby, live): the subject left out.
				"Have been referred by a current employee at Leap? If so, please list their name."},
			Neg: []string{"Have you applied before?", "Did you refer anyone?", "Candidates who have been referred by an employee are reviewed first."},
		},
		248: {
			Pos: []string{"Referred by a current employee (yes/no)", "Candidate referral by employee"},
			Neg: []string{"If referred by a current employee, please provide the name.", "When referred by an employee, enter the name"},
		},
		249: {
			Pos: []string{"Do you know anyone who works at Example Corp?", "Do you know anyone employed with Example Corp?"},
			Neg: []string{"Do you know any engineering tools?", "Do you know anyone in the area?"},
		},
		262: {
			Pos: []string{"May we keep your resume for consideration for other positions? Please give your consent.", "May we keep you under consideration for other roles? Please provide your consent."},
			Neg: []string{"Do you consent to the privacy policy?", "Consider other roles"},
		},
		263: {
			Pos: []string{"Are you willing to be considered for other roles?", "Would you like to be considered for future openings?", "I am interested to be considered for additional positions"},
			Neg: []string{"Are you willing to be considered for a raise?", "Would you like to relocate?"},
		},
		264: {
			Pos: []string{"I consent to Example Corp using my personal data for other roles worldwide", "Consent to process and transfer personal information globally"},
			Neg: []string{"I consent to Example Corp using my personal data", "Share personal data worldwide"},
		},
		268: {
			Pos: []string{"To filter automated submissions, select option B if you are a human.", "To stop automated submissions please select option B as a human"},
			Neg: []string{"Select option B", "Do you want to stop automated emails?"},
		},
		269: {
			Pos: []string{"Select option B to confirm you are applying as a human", "Please select option B, I am not a bot"},
			Neg: []string{"Select option A as a human", "Select option B"},
		},
		277: {
			Pos: []string{"I agree to abide by the Non-Disclosure Agreement.", "I will comply with the terms of the non disclosure agreement"},
			Neg: []string{"Do you have a non-disclosure agreement with a prior employer?", "I agree to the terms"},
		},
		278: {
			Pos: []string{"I have read the applicant non-disclosure agreement", "I accept the nondisclosure agreement", "I acknowledge the non-disclosure agreement"},
			Neg: []string{"Have you ever signed a non-disclosure agreement?", "I have read the privacy policy"},
		},
		335: {
			Pos: []string{"Are you a director, officer or investor holding greater than 5% of Example Corp?", "Is any advisor of yours greater than 5%?"},
			Neg: []string{"Are you a director of a board?", "Do you hold more than 5% equity?"},
		},
		344: {
			Pos: []string{"Are you over the age of 18?", "Are you at least 18?"},
			Neg: []string{"Are you at least 21 years old?", "Do you have 18 apples?"},
		},
		345: {
			Pos: []string{"Are you 18 years old?", "Are you 18 or older?"},
			Neg: []string{"Are you 21 years old?", "Are you 18?"},
		},
		346: {
			Pos: []string{"Are you of the legal age to work?", "Legal age to work in your state"},
			Neg: []string{"Are you of age?", "Legal work status"},
		},
		347: {
			Pos: []string{"Are you of legal age?", "Are you of legal working age?", "If hired, can you furnish proof of age?"},
			Neg: []string{"Are you of age?", "Legal age to work", "Can you provide proof of address?"},
		},
		559: {
			Pos: []string{"Have you entered into any agreement or understanding that will in any way prevent you from joining Example Corp or from fully performing the services required by the position?",
				"Is there any contract that would restrict you from working here?",
				"Have you signed any agreements with your current or former employer(s) that could restrict your ability to work for GDIT, compete with these employers, or solicit their customers or business ?"},
			Neg: []string{"Do you agree to the terms that prevent you from sharing confidential data?", "Have you entered into an agreement with a recruiter?"},
		},
		558: {
			Pos: []string{"Can you provide verification of your identity upon hire?", "If hired, will you be able to provide documentation of your identity?"},
			Neg: []string{"Verify your identity", "Enter the code to verify your identity", "Please describe your identity"},
		},
		348: {
			Pos: []string{"Are you above the minimum age?", "Do you meet the minimum working age?"},
			Neg: []string{"What is your maximum age?", "Do you meet the minimum wage?"},
		},
		349: {
			Pos: []string{"Are you over the age of 21?", "Are you at least 21?"},
			Neg: []string{"Are you over the age of 18?", "Are you 22?"},
		},
		350: {
			Pos: []string{"Are you 21 years old?", "Are you 21 or older?"},
			Neg: []string{"Are you 18 years old?", "Are you 21?"},
		},
		351: {
			Pos: []string{"Are you willing to take a drug test?", "Do you consent to a background check?", "Background investigation consent",
				// Crusoe (Ashby, owner workspace).
				"This position is designated a safety-sensitive position and/or is located in a safety-sensitive facility. Are you willing to participate in a drug/alcohol program?",
				"Are you willing to submit to a drug screening?", "Are you able to pass a pre-employment drug screen?", "Will you consent to drug and alcohol testing?"},
			Neg: []string{"Are you willing to travel?", "Do you have a clear history?", "Have you worked on drug discovery programs?"},
		},
		352: {
			Pos: []string{"I authorize monitoring of my MVR", "Verifiable driving experience of two years", "I understand revocation of driving privileges"},
			Neg: []string{"Do you have a valid driver's license?", "Do you have driving experience?"},
		},
		309: {
			Pos: []string{"Have you ever had any disciplinary action taken on your professional license, certification, or credentials?", "Has a professional license you held ever been revoked or suspended?"},
			Neg: []string{"Do you hold a professional license?", "Has your driver's license ever been suspended?", "Has a state license been revoked by a health care board?"},
		},
		353: {
			Pos: []string{"Do you hold a professional license?", "Do you possess any state certification?"},
			Neg: []string{"Do you hold a state driver license?", "Do you hold a valid passport?"},
		},
		354: {
			Pos: []string{"Professional license number", "Industry certifications"},
			Neg: []string{"State driver's license number", "Library card number"},
		},
		355: {
			Pos: []string{"Licenses, certifications", "License / certification"},
			Neg: []string{"Driving license, certification of a vehicle", "Certifications"},
		},
		356: {
			Pos: []string{"Are you FINRA registered?", "FINRA CRD number"},
			Neg: []string{"Are you registered to vote?", "Do you work in finance?"},
		},
		357: {
			Pos: []string{"Are you registered with the Financial Industry Regulatory Authority?"},
			Neg: []string{"Are you registered with a regulatory body?"},
		},
		358: {
			Pos: []string{"Are you a registered representative?", "Are you a licensed broker-dealer?"},
			Neg: []string{"Are you a registered nurse?", "Are you a sales representative?"},
		},
		365: {
			Pos: []string{"Have you ever been terminated from a job?", "Were you ever fired from a position?", "Have you been dismissed by an employer?"},
			Neg: []string{"Have you applied here before?", "Was your lease terminated by the vendor?"},
		},
		366: {
			Pos: []string{"Have you been terminated or suspended?", "Fired and suspended"},
			Neg: []string{"Have you been terminated?", "Have you been suspended?"},
		},
		367: {
			Pos: []string{"Have you been suspended from any previous employment?", "Were you suspended by a former employer?"},
			Neg: []string{"Were you suspended from school?", "Do you have previous employment?"},
		},
	}
}

package match

func routeCasesFixedAConflict() map[int]ruleCases {
	return map[int]ruleCases{
		374: {
			Pos: []string{"Are you a senior government official?", "Are you currently a Senior Government Official?"},
			Neg: []string{"Are you a government employee?", "Are you a senior engineer?"},
		},
		375: {
			Pos: []string{"Have you served as a senior government official?", "Do you serve as a senior government official?"},
			Neg: []string{"Have you served as a government contractor?", "Have you served in the military?"},
		},
		376: {
			Pos: []string{"Did a senior government official refer you?", "Has a senior government official referred you to Example Corp?"},
			Neg: []string{"Did a colleague refer you?", "Are you a senior government official?"},
		},
		377: {
			Pos: []string{"Do you have a family relationship with a Senior Government Official?", "Close personal contact with a senior government official"},
			Neg: []string{"Do you have a family relationship with an employee?", "Do you know a senior government official?"},
		},
		378: {
			Pos: []string{"Did a senior executive of a customer refer you?", "Has a senior executive at a vendor referred you?"},
			Neg: []string{"Did a senior executive refer you?", "Did a customer refer you?"},
		},
		379: {
			Pos: []string{"A senior executive of a client who referred you", "Senior executive of a third-party who referred you"},
			Neg: []string{"Were you referred by a senior executive of Example Corp?", "Were you referred by a client?"},
		},
		380: {
			Pos: []string{"Do you have a family member who is a senior executive at a customer?", "Family relationship with a senior executive of a vendor"},
			Neg: []string{"Do you have a family member who is a senior executive?", "Do you have a family member at a vendor?"},
		},
		381: {
			Pos: []string{"Are you an officer or director of any client of Example Corp?", "Board member of a client"},
			Neg: []string{"Are you a senior executive or director of a client?", "Are you a director of a company?"},
		},
		382: {
			Pos: []string{"Is any client of yours an officer of Example Corp?", "Client of Example Corp where you are a board member"},
			Neg: []string{"Is a senior government official or officer a client?", "Is any client a friend?"},
		},
		383: {
			Pos: []string{"Have you worked for or been employed by a client of Example Corp?", "Have you worked with a client of the firm?"},
			Neg: []string{"Have you worked for Example Corp?", "Do you have a client?"},
		},
		384: {
			Pos: []string{"Have you been disciplined by an administrative agency?", "Were you sanctioned by an administrative agency?"},
			Neg: []string{"Have you been disciplined at school?", "Have you worked for an administrative agency?"},
		},
		385: {
			Pos: []string{"Have you been the subject of an administrative order?", "Subject of an administrative proceeding"},
			Neg: []string{"Have you been the subject of a survey?", "Administrative assistant"},
		},
		386: {
			Pos: []string{"Have you been barred by a professional body?", "Were you fined by a licensing authority?", "Were you debarred by a government agency?"},
			Neg: []string{"Were you fined by a healthcare regulatory board?", "Have you been fined?"},
		},
		387: {
			Pos: []string{"Have you been disciplined by any professional organization?", "Were you reprimanded by any licensing board?", "Have you been disciplined by any professional association?"},
			Neg: []string{"Have you been disciplined by your parents?", "Have you been licensed?", "Have you been sanctioned by any regulatory agency for health care fraud?"},
		},
		388: {
			Pos: []string{"Do you hold any fiduciary appointments?", "Fiduciary capacity for a customer"},
			Neg: []string{"Do you hold any appointments?", "Fiduciary duty"},
		},
		389: {
			Pos: []string{"Are you an executor or trustee for any customer?", "Are you a guardian acting in a fiduciary role?"},
			Neg: []string{"Are you an executor?", "Are you a trustee of a school?"},
		},
		390: {
			Pos: []string{"Do you serve on a board of directors?", "Do you serve as a member on an advisory board?", "Do you sit on a trustee board?"},
			Neg: []string{"Board Software Engineer", "Do you board a plane often?"},
		},
		391: {
			Pos: []string{"Are you on an advisory board for a non-profit?", "Board of directors of a for-profit organization"},
			Neg: []string{"Do you work for a non-profit?", "Is the board of directors meeting?"},
		},
		392: {
			Pos: []string{"Are you currently a member of any board?", "Are you a member of a school board?"},
			Neg: []string{"Are you a member of a union?", "Do you board a plane?"},
		},
		397: {
			Pos: []string{"Do you have outside activities that could be considered competitive?", "List outside activities in conflict"},
			Neg: []string{"Do you have hobbies?", "Do you have competitive hobbies?"},
		},
		398: {
			Pos: []string{"Any competitive pursuits or outside activities?", "Conflicting interests from outside activities"},
			Neg: []string{"Do you have outside plans?", "Any competitive pursuits?"},
		},
		399: {
			Pos: []string{"Do you hold a 25% ownership interest in any business?", "Ten percent or more ownership interest", "10% ownership interest"},
			Neg: []string{"Do you hold 25% of anything?", "Do you have an ownership interest?"},
		},
		400: {
			Pos: []string{"Do you have an ownership interest in a for-profit business?", "Ownership interest in any for profit business"},
			Neg: []string{"Do you have an ownership interest in a house?", "Do you have a for-profit business?"},
		},
		401: {
			Pos: []string{"Do you hold a position of control?", "Position of control in a company"},
			Neg: []string{"Do you hold a position?", "Do you have control?"},
		},
		402: {
			Pos: []string{"Are you an elected official?", "Are you an appointed member of a commission?"},
			Neg: []string{"Are you an elected official or a senior government official?", "Are you elected to the club?"},
		},
		404: {
			Pos: []string{"Do you work for a government entity?", "Are you part of the city council?", "Member of a school board"},
			Neg: []string{"Are you a senior government official at a public agency?", "Are you on a political party committee for a political campaign?", "Do you work for a private entity?"},
		},
		405: {
			Pos: []string{"Are you an employee of any federal, state, or local government?", "Have you worked for a state government?"},
			Neg: []string{
				"Are you a senior government official employed by a federal government?",
				"Have you been employed by a federal government agency? I attest that I have no restrictions",
				"Have you been employed by a federal government agency? meI attest that I have no restrictions",
				"Do you work in the federal market?",
				// Snowflake (Ashby, live).
				"Have you ever been directly employed by (1) any government or military entity, state-owned enterprise, or publicly-funded institution, or (2) a government contractor in a role that recommended Snowflake as part of Government procurement?",
			},
		},
		407: {
			Pos: []string{"Are you or a member of your immediate family a government official?", "Is an immediate family member a government official?",
				"Please advise: Whether a family member or anyone with whom you have a close personal relationship is a government official with regulatory authority over Example Corp."},
			Neg: []string{"Is a family friend a government official?", "Is an immediate family member a teacher?", "Whether a family member is a government contractor."},
		},
		568: {
			Pos: []string{"Is any member of your immediate family a current employee of a state government or political party?",
				"Does a family member work for a candidate for political office?"},
			Neg: []string{"Is any member of your immediate family a current employee of the U.S. Federal Government?",
				"Have you been employed by a member of a royal family?"},
		},
		569: {
			Pos: []string{"Have you ever been employed by a political party, a member of a royal family, or a candidate for political office?",
				"Please select your political party or royal family employment status?"},
			Neg: []string{"Is any member of your household employed by a political party?", "Have you volunteered for a political party?"},
		},
		570: {
			Pos: []string{"Is any member of your family or household a current Example Corp employee or member of the Board of Directors?"},
			Neg: []string{"Is any member of your family a current employee of an organization that is an Example Corp client?",
				"Is any member of your family a current employee of a state government?"},
		},
		566: {
			Pos: []string{"To identify situations that may pose a conflict, please advise if you would be engaging in any of the above listed situations or activities during your employment.",
				"Would any of the following situations create a conflict of interest for you?"},
			Neg: []string{"Do you have a conflict of interest?", "Would you be engaging in any of the above listed activities?"},
		},
		409: {
			Pos: []string{"Do you have government employment experience?", "Have you held a government job?"},
			Neg: []string{"I attest that I have no post-government employment restrictions", "meI certify that I have no post-government employment restrictions", "I am a senior government official in government service"},
		},
		410: {
			Pos: []string{"Do you hold a position as a government official?", "Have you held a role as a government official?"},
			Neg: []string{"Do you hold a driver license?", "Do you know a government official?"},
		},
		411: {
			Pos: []string{"Were you referred by a government official?", "Were you recommended by a government official?"},
			Neg: []string{"Were you referred by a colleague?", "Do you know a government official?"},
		},
		412: {
			Pos: []string{"Are you related to a government official?", "Do you have a close personal relationship with a government official?"},
			Neg: []string{"Are you related to a founder?", "Is a government official reading this?"},
		},
		413: {
			Pos: []string{"Do you engage in outside business activities?", "Outside business activity disclosure"},
			Neg: []string{"Do you have outside interests?", "Do you run a business?"},
		},
		414: {
			Pos: []string{"Do you have an investment in a private company?", "Investments in private companies"},
			Neg: []string{"Do you have an investment in a public fund?", "Do you own a company?"},
		},
		415: {
			Pos: []string{"Have you invested in a current competitor?", "Any investment in a competitor of Example Corp"},
			Neg: []string{"Have you invested in a savings plan?", "Is a competitor hiring?"},
		},
		416: {
			Pos: []string{"Do you have intellectual property ownership you wish to retain?", "Intellectual property rights"},
			Neg: []string{"Do you respect intellectual curiosity?", "Property ownership"},
		},
		418: {
			Pos: []string{"Are you employed by the federal government?", "Have you been an employee by the U.S. government?", "Employment by a state or local government"},
			Neg: []string{"Are you employed by Example Corp?", "I attest that employment by the federal government is disclosed", "Are you a senior government official employed by the state government?"},
		},
		419: {
			Pos: []string{"Do you own any patents you wish to retain?", "Trademarks that you created"},
			Neg: []string{"Do you have a patent attorney?", "Do you own a house?"},
		},
		420: {
			Pos: []string{"Do you hold a paid position on a political campaign?", "Volunteer on a political campaign", "Any position on a political campaign"},
			Neg: []string{"Do you follow politics?", "Do you like campaigns?"},
		},
		421: {
			Pos: []string{"Have you signed a non-disclosure agreement with the U.S. government in a SETA capacity?", "Non disclosure agreement with the US government in an advisory role"},
			Neg: []string{"Have you signed a non-disclosure agreement with a vendor?", "Non-disclosure agreement with the U.S. government"},
		},
		422: {
			Pos: []string{"Have you signed a nondisclosure agreement with the U.S. government?", "Signed an NDA with the US government"},
			Neg: []string{"Have you signed a lease?", "Have you signed an NDA with a vendor?"},
		},
		423: {
			Pos: []string{"NDA with the government in a SETA role", "Any NDA with a government in an advisory capacity"},
			Neg: []string{"NDA with the government", "NDA for the team"},
		},
		424: {
			Pos: []string{"Is a parent, spouse, child, sibling or domestic partner a current employee who serves as an external financial auditor?", "Is your spouse an employee serving as auditor?"},
			Neg: []string{"Is your spouse an employee?", "Is your parent a financial auditor?"},
		},
		425: {
			Pos: []string{"Do you have a family member who is an employee of the audit firm and an auditor?", "Close personal contact, an employee, who is an auditor"},
			Neg: []string{"Do you have a family member who is an employee?", "Do you have a close friend who is an auditor?"},
		},
	}
}

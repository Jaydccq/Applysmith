package match

func routeCasesFixedAOffice() map[int]ruleCases {
	return map[int]ruleCases{
		171: {
			Pos: []string{"Are you able to work in the location listed for this role?", "Are you willing to work at the location of this position?"},
			Neg: []string{"Are you authorized to work in the location listed for this role?", "Are you able to work remotely?",
				"Are you able to work from the stated location of this role?"},
		},
		142: {
			Pos: []string{"Are you able to work in the office?", "Are you able to come in to the office?", "Are you able to go to our office daily?",
				"Can you work in the Springfield, IL office Monday through Wednesday? (Example Corp keeps a hybrid schedule)",
				"Are you able, willing, and excited to work full-time in an office?",
				// Pika (Ashby).
				"Are you able to regularly work from our Palo Alto HQ?",
				// Character (Ashby).
				"Are you open to working full-time in our New York or Redwood City, CA offices?"},
			Neg: []string{"Are you able to work remotely?", "Are you able to lift 20 pounds in a warehouse?", "Are you able and excited to work with our team?",
				"Are you able to work with our HQ team remotely?"},
		},
		144: {
			Pos: []string{"Are you comfortable commuting to this job's location?", "Can you commute to the office?"},
			Neg: []string{"Are you comfortable commuting by bike?", "Do you commute often?"},
		},
		145: {
			Pos: []string{"Are you able to work from our office three days a week?", "Able to work from the office 5 days per week"},
			Neg: []string{"Are you able to work from home five days a week?", "Are you able to work from the office?"},
		},
		146: {
			Pos: []string{"Can you work from the office 4 days a week?", "Can you work out of our office two days per week?",
				// Ambience (Ashby): the requirement is a gerund.
				"This role requires working from our San Francisco office at least 3 days per week, beginning no later than 1 month after accepting an offer. Are you able to meet this requirement?",
				// Lightfield (Ashby, live): the office is the HQ, the days in brackets.
				"Can you work from our San Francisco HQ (4 days a week)?", "Can you work out of our headquarters 3 days per week?"},
			Neg: []string{"Can you work from home 4 days a week?", "Can you work from the office?", "Are you working from home 3 days per week?", "Can you work from our HQ?"},
		},
		147: {
			Pos: []string{"This role is hybrid, 3 days in the week.", "In-office 3 days", "Hybrid with two times",
				// CLEAR (Greenhouse, live).
				"This role is based out of our Headquarters in NYC and has a 5 days in-office expectation. Are you comfortable with this schedule?"},
			Neg: []string{"This role is remote, 3 days of training", "Hybrid schedule", "We offer 5 days of onboarding"},
		},
		148: {
			Pos: []string{"Are you available 3 days a week in the office?", "Come in four days per week to the office"},
			Neg: []string{"Are you available 3 days a week from home?", "Are you available at the office?"},
		},
		149: {
			Pos: []string{"Are you comfortable with this hybrid role?", "Do you accept a hybrid work arrangement? hybrid arrangement",
				// Glean (Greenhouse).
				"Are you willing and able to commit to the hybrid policy if hired?"},
			Neg: []string{"Is this a hybrid car?", "Do you work remotely?", "This is a hybrid position out of our Atlanta office. Do you currently reside in Atlanta?",
				"Have you previously worked in a remote or hybrid environment?", "Would you be interested in a hybrid role based in Austin, TX?"},
		},
		150: {
			Pos: []string{"Are you willing to be working onsite at least 3 days?", "Can you come in onsite every week?", "Are you able to report on-site on Mondays?"},
			Neg: []string{"Can you work remotely?", "Are you willing to work offsite?"},
		},
		151: {
			Pos: []string{"3 days a week onsite is expected", "Are you available 2 days per week in the office?", "Are you okay with 3 days a week working on-site?"},
			Neg: []string{"Can you be remote 3 days a week?", "Can you be onsite?", "We work 3 days a week on-site. Will you require relocation assistance?",
				"This is a hybrid position out of our Atlanta office, 3 days a week in the office. Do you currently reside in Atlanta?"},
		},
		152: {
			Pos: []string{"Are you willing to work onsite?", "Are you comfortable reporting on-site?"},
			Neg: []string{"Are you willing to work remotely?", "Do you work on a site?"},
		},
		153: {
			Pos: []string{"Are you comfortable with 3-4 days in the office?", "Are you ok with two to three days onsite?"},
			Neg: []string{"Are you comfortable with 3-4 days from home?", "Are you comfortable with a team lunch?"},
		},
		154: {
			Pos: []string{"Expect 2-3 days/week in the office", "3-4 days a week at the office", "We expect 2-3 days in the office. Does that work for you?"},
			Neg: []string{"Expect 2-3 days/week from home", "Expect weekly meetings at the office"},
		},
		155: {
			Pos: []string{"Expect 3 days/week in the office", "Attend four days/week at the office"},
			Neg: []string{"Expect 3 days/week at home", "Office hours are 9 to 5"},
		},
		156: {
			Pos: []string{"This is in-person 5 days a week", "Are you able to work in person six days per week?"},
			Neg: []string{"This is remote 5 days a week", "Are you able to meet in person?"},
		},
		157: {
			Pos: []string{"Are you available 5 days a week in person?", "Seven days per week, in-person", "Are you able to work 5 days/week in person at our HQ in Brooklyn, NY?"},
			Neg: []string{"Are you available 5 days a week at home?", "Are you available in person?", "Are you able to work 5 days/week from home?"},
		},
		158: {
			Pos: []string{"This role is on-site 3 days a week", "Onsite two days per week"},
			Neg: []string{"This role is remote 3 days a week", "This role is on-site"},
		},
		159: {
			Pos: []string{"We expect in-person 5x a week", "On-site 3x per week"},
			Neg: []string{"We expect 5x a week sales growth", "In-person meetings are common"},
		},
		160: {
			Pos: []string{"5x a week in person", "3x per week in our office"},
			Neg: []string{"5x a week at home", "Meet in person"},
		},
		161: {
			Pos: []string{"Are you willing to work in the office?", "Are you willing to work out of our office?", "Would you be excited to work from our NYC office 3+ days per week?"},
			Neg: []string{"Are you willing to work from home?", "Are you willing to travel to the office?", "Would you be excited to work from home with occasional office visits?"},
		},
		162: {
			Pos: []string{"Will you be based out of our office 3 days a week?", "Based in the office 2 days per week"},
			Neg: []string{"Will you be based out of our office?", "Based at home 3 days a week"},
		},
		163: {
			Pos: []string{"Are you comfortable working in our office?", "Are you comfortable to work in the office?", "Are you happy being in person every day in our NY office?"},
			Neg: []string{"Are you comfortable working remotely?", "Are you comfortable with office politics?", "Are you happy being contacted by our office about other roles?"},
		},
		164: {
			Pos: []string{"Are you comfortable in an onsite work environment?", "In-person setting is required"},
			Neg: []string{"Which onsite environment location would you prefer?", "What location would you choose for the in-person workplace?", "Is the work environment friendly?"},
		},
		165: {
			Pos: []string{"This role will be physically located in Springfield", "This position is in-person", "This job is on-site"},
			Neg: []string{"This role will be remote", "This role is exciting",
				// Orkes (Greenhouse, live): the role's place, then where the applicant lives now.
				"This role is located in Santa Clara, CA or Bellevue, WA. Are you currently located within a drivable distance of either of those areas?",
				// Clera (Ashby, live): the place a work authorization is asked for, not the office.
				"Do you have work authorization to work in the country where this job is located?",
				"This job will be physically based in the office location indicated on this job description. Which of the following describes your current or future situation?"},
		},
	}
}

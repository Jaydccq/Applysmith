package match

import "testing"

// The contains-answer rung does not run on a protected category: a label that
// merely contains the answer names another group. Rows are traps (score 0) and
// the equivalences that must keep working.
func TestOptionScore_ProtectedCategoryNeverMatchesByContainment(t *testing.T) {
	const noWhy = ""
	runScoreCases(t, []scoreCase{
		// Gender: whole labels within {male, man} and {female, woman} only.
		{"PO-018", "Male", "Transgender male", "Gender", 0, "whole label"},
		{"PO-018", "Male", "Cisgender male", "Gender", 0, "whole label"},
		{"PO-018", "Female", "Transgender female", "Gender", 0, "whole label"},
		{"PO-018", "Man", "Trans man", "Gender", 0, "whole label"},
		{"PO-018", "Woman", "Non-binary woman", "Gender", 0, noWhy},
		{"PO-018", "Male", "Male (cisgender)", "Gender", 0, "whole label"},
		{"PO-018", "Male", "Female", "Gender", 0, noWhy},
		{"PO-018", "Male", "Man", "Gender", 0.95, "gender-equivalent"},
		{"PO-018", "Woman", "Female", "Gender", 0.95, "gender-equivalent"},
		{"PO-018", "Male", "Male", "Gender", 1, "exact"},
		// A label that lists only one group's words is that group (Faire), and
		// any other word still names another one.
		{"PO-018", "Male", "Man, male or masculine", "Gender", 0.95, "gender-equivalent"},
		{"PO-018", "Woman", "Woman, female or feminine", "Gender", 0.95, "gender-equivalent"},
		{"PO-018", "Male", "Transgender man, male, or masculine", "Gender", 0, noWhy},
		{"PO-018", "Man", "Woman, female or feminine", "Gender", 0, noWhy},
		// Orientation: a label that lists only orientations names each of them
		// (Gusto); a word outside the list, or the answer missing, is another one.
		{"PO-018", "Bisexual", "Bisexual, pansexual and/or queer", "Sexual orientation", 0.95, "orientation-listed"},
		{"PO-018", "Gay", "Gay and/or lesbian", "Sexual orientation", 0.95, "orientation-listed"},
		{"PO-018", "Lesbian", "Gay and/or lesbian", "Sexual orientation", 0.95, "orientation-listed"},
		{"PO-018", "Heterosexual", "Bisexual, pansexual and/or queer", "Sexual orientation", 0, noWhy},
		{"PO-018", "Bisexual", "Bisexual (questioning)", "Sexual orientation", 0, noWhy},
		{"PO-018", "Bisexual", "Bisexual", "Sexual orientation", 1, "exact"},
		// A hyphen that splits the word is the same word (Klaviyo's "Bi-Sexual").
		{"PO-018", "Bisexual", "Bi-Sexual", "Sexual orientation", 1, "hyphen-folded"},
		{"PO-018", "Heterosexual", "Bi-Sexual", "Sexual orientation", 0, noWhy},
		// Race and ethnicity: a negated parenthetical qualifier is not part of
		// the match; any other extra word is another group.
		{"PO-018", "Asian", "Asian (Not Hispanic or Latino)", "Race", 0.75, "contains-answer"},
		{"PO-018", "White", "White (Not Hispanic or Latino)", "Race", 0.75, "contains-answer"},
		{"PO-018", "Asian", "East Asian", "Race", 0, "whole label"},
		// A parenthetical that lists members is not part of the match either (Two Sigma on Avature).
		{"PO-018", "East Asian", "East Asian (including Chinese, Japanese, Korean, Mongolian, Taiwanese)", "Race", 0.7625, "contains-answer"},
		{"PO-018", "South Asian", "South Asian (e.g. Indian, Pakistani)", "Race", 0.7833, "contains-answer"},
		{"PO-018", "Asian", "East Asian (including Chinese)", "Race", 0, "whole label"},
		// Members listed with no "including" still list members (Smartsheet);
		// a single one in brackets is a subgroup, another group.
		{"PO-018", "East Asian", "East Asian (Chinese / Hong-Kong / Japanese / Korean / Mongolian / Tibetan / Taiwanese)", "Race", 0.75, "contains-answer"},
		{"PO-018", "Asian", "East Asian (Chinese / Japanese)", "Race", 0, "whole label"},
		{"PO-018", "Asian", "Asian (Chinese)", "Race", 0, "whole label"},
		{"PO-018", "Indian", "American Indian or Alaska Native", "Race", 0, "whole label"},
		{"PO-018", "Indian", "Asian Indian", "Race", 0, "whole label"},
		// The owner's decision (docs/architecture/apply.md, Postscript): a label that lists the recorded race among
		// other groups takes it; a group whose name only contains it does not.
		{"PO-018", "Native American", "Native American or Alaska Native", "Race", 0.9, "lists the answer"},
		{"PO-018", "Black", "Black or African American", "Race", 0.9, "lists the answer"},
		{"PO-018", "Asian", "Asian or Pacific Islander (including East Asian, South Asian and Southeast Asian)", "Race", 0.9, "lists the answer"},
		{"PO-018", "Asian", "Asian / Pacific Islander", "Race", 0.9, "lists the answer"},
		{"PO-018", "Hispanic or Latino", "Hispanic, Latino or Spanish origin", "Race", 0.9, "lists the answer"},
		{"PO-018", "Asian", "Native Hawaiian or Other Pacific Islander", "Race", 0, "whole label"},
		{"PO-018", "Hispanic or Latino", "Hispanic or Latino (of any race)", "Race", 0, "whole label"},
		{"PO-018", "White", "Not white", "Race", 0, "polarity"},
		{"PO-018", "Hispanic", "Not Hispanic or Latino", "Race", 0, "polarity"},
		{"PO-018", "Black or African American", "Black or African American", "Race", 1, "exact"},
		// An EEO-1 definition is not part of the match either (Instacart), but it
		// does not widen the whole-label rule.
		{"PO-018", "Asian or Asian-American", "Asian or Asian-American (A person having origins in any of the original peoples of Asia)", "Race", 0.7625, "contains-answer"},
		{"PO-018", "Asian", "Asian or Asian-American (A person having origins in any of the original peoples of Asia)", "Race", 0.9, "lists the answer"},
		// Veteran status.
		{"PO-007", "Veteran", "Spouse of a veteran", "Veteran status", 0, noWhy},
		{"PO-007", "Veteran", "Disabled veteran", "Veteran status", 0, "disability"},
		{"PO-007", "Protected veteran", "Not a protected veteran", "Veteran status", 0, "stance"},
		{"PO-007", "Veteran", "Not a veteran", "Veteran status", 0, "stance"},
		{"PO-007", "I am a protected veteran", "I identify as one or more of the classifications of protected veteran", "Veteran status", 0.98, "stance"},
		// Disability.
		{"PO-007", "Disability", "Family member of a person with a disability", "Disability", 0, noWhy},
		{"PO-007", "Disability", "No disability", "Disability", 0, "stance"},
		{"PO-007", "Disabled", "Not disabled", "Disability", 0, "stance"},
		{"PO-007", "Yes, I have a disability", "No, I do not have a disability", "Disability", 0, "stance"},
		{"PO-007", "Yes, I have a disability", "Yes, I have a disability (or have had one)", "Disability", 0.98, "stance"},
	})
}

// A "Yes" that is put off or made conditional does not answer a now-question,
// and "Not yet" is a soft no: neither is chosen for the user.
func TestOptionScore_DeferredYesIsNeverChosen(t *testing.T) {
	const now = "Have you graduated?"
	const sponsor = "Do you currently require sponsorship?"
	runScoreCases(t, []scoreCase{
		{"PO-015", "Yes", "Not yet, but yes in 2027", now, 0, "soft no"},
		{"PO-015", "Yes", "Not currently", now, 0, "soft no"},
		{"PO-015", "Yes", "Yes, but I will need sponsorship later", sponsor, 0.4, "deferred"},
		{"PO-015", "Yes", "Yes, if the role is remote", now, 0.4, "deferred"},
		{"PO-015", "Yes", "Yes, in 2027", now, 0.4, "deferred"},
		// A question that asks about a later time can be answered with one.
		{"PO-015", "Yes", "Yes, but I will need sponsorship later", "Will you require sponsorship later?", 0.85, "yes-variant"},
		// The plain options keep their scores, and the question text is optional.
		{"PO-015", "Yes", "Yes", now, 1, "exact"},
		{"PO-015", "Yes", "Yes, I have graduated", now, 0.85, "yes-variant"},
		{"PO-015", "Yes", "Yes, but I will need sponsorship later", "", 0.85, "yes-variant"},
		{"PO-015", "No", "Not yet, but yes in 2027", now, 0, "YES variant"},
	})
}

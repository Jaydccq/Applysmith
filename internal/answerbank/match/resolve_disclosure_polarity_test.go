package match

import "testing"

// TestSponsorshipPolarity_ClosedShapes is the closed-shape requirement of "Work
// authorization and sponsorship": only a recognised shape is read, and nothing
// outside the vocabulary or the shapes is ever a plain question by default.
func TestSponsorshipPolarity_ClosedShapes(t *testing.T) {
	cases := []struct {
		text, company string
		negated, ok   bool
	}{
		{"Do you require sponsorship?", "", false, true},
		// Groupon (Greenhouse, Milan): "there" is the place the question before it named.
		{"Will you now or in the future require employer sponsorship to work there?", "", false, true},
		// NXP (Workday, live): an invitation to ask the company leads, and quotation marks set off the term.
		{"(Please ask us if you are uncertain whether you may need immigration sponsorship or desire clarification.) Will you now or in the future require “sponsorship for an immigration-related employment benefit?\"\"\"", "NXP Semiconductors", false, true},
		{"Please contact us if you have questions. Are you able to work in the US without sponsorship?", "", true, true},
		// Shield AI (Lever, live): the ability to work is the right to work.
		{"Do you have the ability to work in the US without the need for sponsorship?", "", true, true},
		{"Please ask us if you are not sure whether you need sponsorship.", "", false, false},
		// A list of visa categories in parentheses is an illustration, and "continue" is inert.
		{"Will you now or in the future require sponsorship for employment visa status (e.g., F1, H-1B visa status)?", "", false, true},
		// Ambience (Ashby, live): "the location the job is based" is the role's place.
		{"Will you now or in the future require sponsorship to work in the location the job is based?", "", false, true},
		{"Will you now or in the future require sponsorship to work in the location where the job is based?", "", false, true},
		// Flock (Ashby, live): the posting calls the company "Flock"; the question writes "Flock Safety".
		{"Will you now or in the future require Flock Safety to commence (\"sponsor\") an immigration case in order to employ you?", "Flock", false, true},
		{"Will you require Acme Never sponsorship?", "Acme", false, false},
		// Zinnia (Greenhouse, live): "examples but not limited to:" opens an illustration, and a student visa is a visa.
		{"Will you now or in the future require visa sponsorship? (examples but not limited to: H1B, F-1 Student Visa, etc.)", "Zinnia", false, true},
		{"Will you now or in the future require visa sponsorship? (examples but not limited to: a transfer of your H1B)", "Zinnia", false, false},
		// Ivo (Ashby, live): "currently" between "you" and "authorized".
		{"Are you currently authorized to work in the United States without employer sponsorship?", "Ivo", true, true},
		{"Are you currently authorized to work in the United States without employer sponsorship now or in the future?", "Ivo", false, false},
		// Backblaze (Greenhouse, live): "permanent employment" is the job's tenure; permanent residence is another fact.
		{"Do you require sponsorship, now or in the future, for permanent employment? (e.g., H-1B visa status)?", "Backblaze", false, true},
		{"Do you require sponsorship, now or in the future, for permanent residence?", "Backblaze", false, false},
		// Julius (Ashby, live): two verbs and two nouns of the one requirement.
		{"Will you now or in the future require Julius to sponsor or support a visa or other employment authorization to work in the United States?", "Julius", false, true},
		// Snorkel AI (Greenhouse, live): three aid nouns and "to, or otherwise, work".
		{"Will you require sponsorship or other assistance or support to be authorized to, or otherwise, work from the US?", "Snorkel AI", false, true},
		// Corelight (Greenhouse, live): a three-item list of what is sponsored,
		// and "unrestricted" authorization that the "without sponsorship" defines.
		{"Will you now or in the future require Corelight to sponsor a visa, work permit, or other employment authorization in the country where this position is based?", "Corelight", false, true},
		{"Do you have unrestricted authorization to work in the country where this position is located, without requiring current or future employer sponsorship?", "Corelight", true, true},
		{"Do you currently have unrestricted authorization to work in the United States?", "Corelight", false, false},
		// Replo (Ashby, live): the time qualifier as a sentence of its own after the question.
		{"Do you need a visa to work in the United States? Anytime now or in the future?", "Replo", false, true},
		{"Do you need a visa to work in the United States? Not now or in the future?", "Replo", false, false},
		// Lighthouse (Greenhouse, live): "and/or" joins the nouns of one requirement.
		{"Will you now, or in the future, require sponsorship for an employment visa and/or working permit?", "Lighthouse", false, true},
		{"Are you able to work without a visa and/or permit sponsorship?", "", true, true},
		// LanceDB (Ashby, live): "authorization" misspelt.
		{"Will you now or in the future require employer sponsorship for employment auhorization (e.g., H-1B visa sponsorship)?", "LanceDB", false, true},
		// project44 (Greenhouse, live): the qualifier as adjectives after "the need for".
		{"Are you legally authorized to work in the U.S. without the need for current or future visa sponsorship?", "project44", true, true},
		// Envoy (Ashby, live): a note that says to answer yes when sponsorship is required restates a plain question.
		{"Will you now or in the future require Envoy to sponsor an immigration case in order to employ you? If you’ll require Envoy to commence, i.e., “sponsor,” an immigration or work permit case in order to employ you, either now or at some point in the near future, then you should answer yes. An example of an immigration or work permit case that may require sponsorship now or in the future would be an H-1B or other employment-based work permit sponsorship.", "Envoy", false, true},
		// The same note under a negated question names Yes for the opposite answer: not read.
		{"Are you able to work in the US without sponsorship? If you will require sponsorship, then you should answer yes.", "", false, false},
		// A note whose condition is not the plain requirement is not set aside.
		{"Will you require sponsorship? If you will not require sponsorship, answer yes.", "", false, false},
		{"Will you require sponsorship? If you will require a visa transfer rather than sponsorship, answer yes.", "", false, false},
		{"Will you require sponsorship? If you require sponsorship, answer yes. Otherwise answer no.", "", false, false},
		// Anduril (Greenhouse, live): "e.g" that lost its last stop still opens an illustration.
		{"Will you require sponsorship from Anduril for employment now or in the future (e.g, H1B visa)?", "Anduril", false, true},
		// Everlaw (Greenhouse, live): a closing bracket the question never opened is a typo at its end.
		{"Will you now or in the future require any type of sponsorship for employment visa status (e.g. H1-B status)?)", "Everlaw", false, true},
		{"Do you require sponsorship? (Yes) or not)", "", false, false},
		{"Do you now or in the future require visa sponsorship to continue working in the United States?", "", false, true},
		// Atoms (Greenhouse, live): "continued" is inert like "continue".
		{"Do you now or will you in the future require immigration sponsorship by the company for continued work-authorization?", "Atoms", false, true},
		// A list that says more than categories (a transfer, a negation) is not an illustration.
		{"Do you require sponsorship to continue working in the US (e.g. H-1B transfer)?", "", false, false},
		{"Will you require sponsorship for employment visa status (not including H-1B)?", "", false, false},
		// GM: the list closes on a catch-all restating the sponsorship; one that narrows it keeps the list.
		{"Will you in the future require immigration sponsorship for work authorization (e.g., H1-B, OPT, STEM OPT, CPT, TN, J-1, or any other work authorization requiring entry of GM as the immigration employer of record on a government form, a written submission, or other immigration support from the company)?", "General Motors", false, true},
		{"Will you require sponsorship (e.g., H-1B, or any other visa except a transfer)?", "", false, false},
		// The company's refusal to sponsor, stated around the question, is set aside; one that conditions or addresses the applicant is not.
		{"We do not sponsor visas, including H-1B, OPT extensions (STEM or otherwise), or any other work authorization. Are you able to work in the US without any form of sponsorship? Note: Example Corp is not sponsoring or transferring work visas at this time.", "Example Corp", true, true},
		{"We do not sponsor visas unless you are a current employee. Do you require sponsorship?", "", false, false},
		{"We do not sponsor visas. We do not sponsor visas.", "", false, false},
		{"Will you require sponsorship for employment visa status (e.g., H-1B visa status)?", "", false, true},
		// "etc." is a list filler: it carries no polarity and its period does not end the sentence.
		{"Will you now or in the future require sponsorship for employment visa status (e.g., H-1B, etc.)?", "", false, true},
		{"Are you able to work in the US without sponsorship (e.g., H-1B, etc.)?", "", true, true},
		{"Do you or will you require sponsorship to work in the U.S.?", "", false, true},
		{"Do you require sponsorship before you could begin working?", "", false, true},
		{"Do you require visa authorization to work in the US?", "", false, true},
		{"Is sponsorship needed now or in the future?", "", false, true},
		// Qvest (Lever, live): "either now or at some point in the future" is the one now-or-future qualifier.
		{"Will you require the company to sponsor an immigration or work permit case in order to employ you, either now or at some point in the future?", "Qvest US", false, true},
		// Bitdeer (Breezy, live): the time qualifier set off inside the noun.
		{"Will you require sponsorship, now or in the future, for work authorization in the United States?", "", false, true},
		{"Visa sponsorship *", "", false, true},
		{"Do you require sponsorship to work for Example Corp?", "Example Corp", false, true},
		// Live wordings: Pinterest's "or other employer assistance to obtain, extend, or maintain", BambooHR's
		// split tenses, and Teamtailor's question title before TixTrack's "commence ("sponsor")" case.
		{"Will you now or in the future require employer sponsorship or other employer assistance to obtain, extend, or maintain authorization to work in the country in which you are applying for a position?", "", false, true},
		// Scale AI (Greenhouse, live): "to retain or extend" is the same purpose.
		{"Will you now or in the future require company sponsorship to retain or extend your work authorization in the country where the job is located?", "Scale AI", false, true},
		// Elastic (Greenhouse embed, live): "to continue or extend" is the same purpose too.
		{"Will you require Elastic's sponsorship to continue or extend your work authorization status?", "Elastic", false, true},
		{"Do you currently require, or will you need in the future, sponsorship to work in the United States?", "", false, true},
		{"Will you now or in the future require employer sponsorship for work authorization (e.g., H-1B or other employment-based sponsorship) to work in the U.S.?", "", false, true},
		// General Mills' "immigration-related employment benefit" and Coterie's "(ex: H1B, E-3, etc...)".
		{"Will you now or in the future require sponsorship for an immigration-related employment benefit?", "", false, true},
		// GDIT: the possessive "company’s".
		{"Will you now or in the future require the company’s sponsorship for an immigration-related employment benefit?", "", false, true},
		{"Will you now or in the future require any kind of visa sponsorship (ex: H1B, E-3, etc...) for employment at Coterie?", "Coterie", false, true},
		{"US Sponsorship Required Will you now or in the future require TixTrack to commence (\u201csponsor\u201d) an immigration case in order for you to obtain, maintain, or extend employment authorization to employ you? (for example, H-1B or other employment-based immigration case)", "TixTrack", false, true},
		// SmartRecruiters' "in the country of hire" is the posting's country, not an unknown word.
		{"Do you now, or will you in the future, require visa sponsorship to work for Example Corp in the country of hire?", "Example Corp", false, true},
		// Expedia (Workday, live): split tenses with no "now", "support or sponsorship" and "begin or continue".
		{"Do you need, or will you need in the future, any immigration related support or sponsorship from Expedia Group to begin or continue employment with Expedia Group?", "Expedia Group", false, true},
		{"Do you need, or will you need in the future, any immigration-related support or sponsorship to start or continue employment?", "", false, true},
		// ERCOT (Workday, live): "do you now, or will you ever" and "to initially accept, or to subsequently continue in, permanent employment".
		{"Do you now, or will you ever require sponsorship for employment visa status in order to initially accept, or to subsequently continue in, permanent employment with ERCOT?", "ERCOT", false, true},
		// Harvey (Ashby, live): "will you now or will you in the future".
		{"Will you now or will you in the future require employment visa sponsorship?", "Harvey", false, true},
		{"Do you now, or will you ever require sponsorship to accept or continue in permanent residency?", "", false, false},
		// Campbell's (Workday, live): the employer's affiliates in parentheses, and "to begin" or "to continue" employment alone.
		{"Will you require sponsorship by The Campbell's Company (or one of its affiliates) to begin employment with the company in the United States?", "Campbell's", false, true},
		{"Will you require sponsorship by The Campbell's Company (or one of its affiliates) to continue employment with the company in the United States?", "Campbell's", false, true},
		{"Will you require sponsorship by The Campbell's Company (or one of its affiliates, unless you hold a green card)?", "Campbell's", false, false},
		// IMC (Greenhouse, live): "to begin working" is the work itself, either polarity.
		{"Will you require immigration sponsorship to begin working for IMC?", "IMC", false, true},
		{"Are you able to begin working for IMC without sponsorship?", "IMC", true, true},
		// Baton (Greenhouse, live): a work visa, its transfer or sponsorship is one requirement; a bare transfer is not read.
		{"Do you now or in the future require a Work Visa, Transfer, or Sponsorship?", "", false, true},
		{"Do you now or in the future require a transfer?", "", false, false},
		// Red Hat (Workday, live): "the country of origin for this position" is the posting's country; the applicant's own is not.
		{"Do you require work authorization to work in the country of origin for this position?", "", false, true},
		{"Do you require work authorization to work in your country of origin?", "", false, false},
		// Solace (Ashby, live): the company asked "to sponsor you for a work permit", "either now or in the future".
		{"Will you require Solace to sponsor you for a work permit, either now or in the future, to work in the United States?", "Solace Health", false, true},
		{"Will you require Solace to sponsor you for a dinner?", "Solace Health", false, false},
		// Mindbody (Greenhouse, live): "will you ever in the future" and "employer-sponsored work authorization".
		{"Do you now, or will you ever in the future, require employer-sponsored work authorization to legally work in the country where this role is located?", "Mindbody", false, true},
		// Fermat (Greenhouse, live): "now or ever in the future" is one future.
		{"Will you require sponsorship now or ever in the future to work in the United States?", "Fermat", false, true},
		// Lightspark (Ashby, live): the company asked "to serve as a sponsor for employment", with a visa list after.
		{"Will you now, or in the future, require Lightspark to serve as a sponsor for employment? (e.g., H-1B visa, T-N visa, O visa, etc)", "Lightspark", false, true},
		{"Will you now, or in the future, require Lightspark to sponsor you for employment?", "Lightspark", false, true},
		{"Will you require Lightspark to serve as a sponsor for a dinner?", "Lightspark", false, false},
		// HP (Workday, live): "the job posting country" is the posting's country.
		{"Will you now, or in the future, require sponsorship for employment to work in the job posting country?", "", false, true},
		// Twitch (Greenhouse, live): a sentence saying the answer is mandatory before the question.
		{"Your response is mandatory when applying for a U.S.-based position. Do you need, or will you need in the future, sponsorship to work in the U.S.?", "Twitch", false, true},
		{"Your response is mandatory if you hold a green card. Do you require sponsorship?", "", false, false},
		// Chime (Greenhouse, live): "immigration support or visa sponsorship" is one requirement.
		{"Do you now or in the future require immigration support or visa sponsorship to continue working in the United States?", "Chime", false, true},
		// Gremlin (Greenhouse, live): an employee's sponsorship is the sponsorship.
		{"Will you now or in the future require employee sponsorship to work in the United States?", "Gremlin", false, true},
		// OpenGov (Ashby, live): "sponsorship or employment authorization" is one requirement.
		{"Do you now or will you in the future require sponsorship or employment authorization to work in the United States?", "OpenGov", false, true},
		// Zoox (Lever, live): the noun said in both clauses of the split qualifier.
		{"Sponsorship: Do you require sponsorship, or will you require sponsorship in the future for employment authorization in the United States?", "Zoox", false, true},
		{"Do you require sponsorship or will you require visa sponsorship in the future?", "", false, false},
		{"Do you require sponsorship, or will you not require sponsorship in the future?", "", false, false},
		// ClickHouse (Ashby, live): a transfer of sponsorship is sponsorship, and
		// the right to work is what it obtains, as work authorization is.
		{"Will you require sponsorship or transfer of sponsorship to obtain or extend your right to work in the United States?", "ClickHouse", false, true},
		{"Will you require sponsorship to obtain the right to work in the United States?", "", false, true},
		{"Will you require sponsorship or transfer of sponsorship but not the right to work?", "", false, false},
		// Mintlify (Ashby, live): a follow-up for a Yes after the question.
		{"Will you now or in the future require visa sponsorship? If yes, select the type of sponsorship.", "Mintlify", false, true},
		// New Relic (Greenhouse, live): the aid beside the sponsorship, a list
		// of nouns for one requirement, and "work for or continue to work for".
		{"Do you now or will you in the future require sponsorship for or assistance with an employment visa, authorization, or permit to work for or continue to work for New Relic in this role (e.g. U.S. F-1, H-1B, TN, E-3, or other non-immigrant visa category)?", "New Relic", false, true},
		{"Do you now or will you in the future require sponsorship for or assistance with an employment visa, authorization, or permit to work for or not continue to work for New Relic?", "New Relic", false, false},
		// DoorDash (Greenhouse, live): the purpose "to attain or maintain your
		// employment eligibility", and an example list closing on "or any ...".
		{"Will you in the future require immigration sponsorship by our company to attain or maintain your employment eligibility (e.g., H-1B, E-3, TN, O-1, STEM OPT, or any immigration work authorization requiring a written submission from the company to a government agency)?", "DoorDash", false, true},
		{"Will you require sponsorship to maintain your employment eligibility?", "", false, true},
		{"Will you require sponsorship (e.g., H-1B, or any visa except a TN)?", "", false, false},
		// Peloton (Greenhouse, live): "now or could you in the future", the qualifier split.
		{"Do you now or could you in the future require Peloton to sponsor you for a visa to work in the country where this position is located?", "Peloton", false, true},
		{"Do you now or would you ever require sponsorship?", "", false, true},
		// Zscaler (Greenhouse, live): "right to work support" is one more name of the aid.
		{"Do you, now or in the future, require a work permit, visa, or additional right to work support for the country where you are applying to work?", "Zscaler", false, true},
		{"Do you require right to work support?", "", false, true},
		{"Do you have the right to work support?", "", false, false},
		// Plaid (Ashby, live): the company asked to provide the aid.
		{"Will you now or in the future require Plaid to provide immigration-related support or sponsorship in order to employ you?", "Plaid", false, true},
		{"Will you now or in the future require Plaid to provide sponsorship?", "Plaid", false, true},
		{"Will you now or in the future require Plaid to provide proof of sponsorship?", "Plaid", false, false},
		// Twitch (Greenhouse, live): the parent company names both the sponsor and the employment.
		{"Your response is mandatory when applying for a U.S.-based position. Do you need, or will you need in the future, any immigration related support or sponsorship from Amazon in order to begin or continue employment with Amazon?", "Twitch", false, true},
		{"Do you need, or will you need in the future, any immigration related support or sponsorship from Amazon in order to begin or continue employment with Hooli?", "Twitch", false, false},
		{"Do you need, or will you need in the future, any support or a green card?", "", false, false},
		{"Do you currently require, or will you need in the future, no sponsorship?", "", false, false},
		// Torc (Greenhouse, live): the verb said once per tense is the one now-or-future qualifier.
		{"Do you require or will you require sponsorship for employment visa status (e.g., H-1B visa status, etc.) to work legally in the United States for TORC Robotics?", "Torc Robotics", false, true},
		{"Do you require or will you not require sponsorship?", "", false, false},
		{"Do you require current or future visa sponsorship?", "", false, true},
		// Lime (Ashby, live): a list of the requirement's nouns closed by "or any other assistance".
		{"Will you now or in the future require visa sponsorship, work authorization, or any other assistance to legally work?", "", false, true},
		{"I do not require current or future visa sponsorship.", "", true, true},
		{"Will you require sponsorship or other help, or a green card?", "", false, false},
		// SpaceXSI (Greenhouse, live): a lead that only names the country asked about.
		{"If working in the US, will you now, or in the future, require sponsorship for employment visa status (e.g., H-1B visa) to legally work in the US?", "SpaceXSI", false, true},
		{"If working in the US, will you require sponsorship or a green card?", "", false, false},
		// Bitdeer (Breezy, live): a note that a citizen of the role's country needs none says nothing a recorded need contradicts.
		{"Will you now or in the future require sponsorship for employment visa status (e.g. H-1B visa, Employment Pass)? Generally, if you are a citizen of the country where the job is located at - you will not require sponsorship of an employment visa.", "Bitdeer", false, true},
		{"Will you require sponsorship? If you are a citizen of the country where the job is located, select No.", "", false, false},
		{"Do you not currently require sponsorship?", "", true, true},
		{"Are you able to work in the US without sponsorship?", "", true, true},
		{"Is no visa sponsorship needed?", "", true, true},
		// A company word that is itself structural never becomes inert.
		{"Do you require sponsorship to work for Never Inc?", "Never Inc", false, false},
		// Not a recognised shape.
		{"Do you require sponsorship to work for Example Corp?", "", false, false},
		{"Do you require sponsorship? Select Yes if you will need it.", "", false, false},
		{"Do you require sponsorship now and later and ever?", "", false, false},
		{"Will you require sponsorship now? Will you in the future?", "", false, false},
		{"Do you require sponsorship, or do you hold a green card?", "", false, false},
		{"Are you eligible to work in the US if you need sponsorship?", "", false, false},
		{"", "", false, false},
	}
	for _, c := range cases {
		negated, ok := sponsorshipPolarity(c.text, c.company)
		if ok != c.ok || (ok && negated != c.negated) {
			t.Errorf("%q: negated=%t ok=%t, want negated=%t ok=%t", c.text, negated, ok, c.negated, c.ok)
		}
	}
}

package toolchain

import "applysmith/internal/documents/assets"

// The synthetic documents of the smoke. They use the fictional persona of the
// specs, reserved example domains and every character TeX treats specially,
// so a compile proves the encoders as well as the toolchain.

// specials is a line that must survive extraction exactly.
const specials = `Special characters: & % $ # _ { } ~ ^ \`

// accented is "Geneve" with a grave accent on the second e, written as bytes
// so the source stays ASCII.
const accented = "Gen\xc3\xa8ve"

// Facts the smoke requires in each text layer.
var (
	resumeRequired = []string{"Alex Example", "alex.example@example.com", specials, "Gen\xc3\xa8ve", "2021-Present", "[Lead] platform migration"}
	coverRequired  = []string{"Alex Example", "alex.example@example.com", specials, "Kind regards,"}
)

func syntheticResume(paper assets.Paper) assets.Resume {
	return assets.Resume{
		Paper:     paper,
		Labels:    assets.EnglishLabels,
		FirstName: "Alex",
		LastName:  "Example",
		Address:   "100 Example Street, Sampleville, Exampleland",
		Phone:     "+1 555 0100",
		Email:     "alex.example@example.com",
		Links: []assets.Link{
			{Label: "Profile", URL: "https://www.example.com/in/alex-example?ref=a&b=2#top"},
			{Label: "Portfolio", URL: "https://example.org/~alex_example/50%25"},
		},
		Profile: "Engineer and analyst with 100% on-time delivery and $5M budgets. " + specials,
		Competencies: []assets.Competency{
			{Label: "Platforms", Text: "Go, SQL, snake_case naming, {braces} and ~approximations"},
			{Label: "Methods", Text: "Estimation, review, and a #1 ranked test discipline"},
			{Label: "Domains", Text: "Logistics in " + accented + ", reporting & forecasting"},
		},
		Experience: []assets.Entry{
			{Dates: "2021-Present", Title: "Senior Engineer", Organization: "Example Systems", Location: accented + ", Exampleland",
				Bullets: []string{
					"[Lead] platform migration for 40% fewer incidents",
					"Cut report time from 5 h to 20 min using a 3-step pipeline",
					"Mentored four engineers in code review and design",
				}},
			{Dates: "2018-2021", Title: "Engineer", Organization: "Sample Works", Location: "Sampleville, Exampleland",
				Bullets: []string{
					"Built the ingestion service used by twelve teams",
					"Owned on-call for a 99.9% availability target",
				}},
			{Dates: "2015--2018", Title: "Analyst", Organization: "Placeholder Labs", Location: "Exampleland",
				Bullets: []string{"Produced weekly forecasts for the planning group"}},
		},
		Education: []assets.Entry{
			{Dates: "2011-2015", Title: "BSc in Mathematics", Organization: "Example University", Location: "Exampleland",
				Note: "Thesis on queueing models at 100% load."},
		},
		Languages: "English (native), Exampleish (fluent).",
	}
}

func syntheticCover(paper assets.Paper) assets.Cover {
	return assets.Cover{
		Paper: paper,
		Name:  "Alex Example",
		Contact: []assets.Link{
			{Label: "alex.example@example.com", URL: "mailto:alex.example@example.com"},
			{Label: "+1 555 0100"},
			{Label: "Profile", URL: "https://www.example.com/in/alex-example"},
		},
		Date:       "October 11, 2026",
		Salutation: "Dear Hiring Team,",
		Blocks: []assets.Block{
			{Text: "I am writing about the Platform Engineer role at Example Systems. " + specials},
			{Text: "My most relevant work, framed for the tasks in your posting:", Bullets: []string{
				"Migration: [Lead] platform migration for 40% fewer incidents",
				"Reporting: cut report time from 5 h to 20 min",
				"Mentoring: four engineers, 2021-Present",
			}},
			{Text: "Your published roadmap for " + accented + " logistics matches the work I have done, and I would like to contribute to it."},
			{Text: "I look forward to hearing from you."},
		},
		Closing: "Kind regards,",
	}
}

package match

import (
	"applysmith/internal/lazyregexp"
)

// Fixed-entry routes, family A: skills, experience gates, disclosures and
// consent flags. Priorities are the catalog's global sort keys.

const (
	faNum  = `(?:\d+|one|two|three|four|five)`
	faNum7 = `(?:\d+|one|two|three|four|five|six|seven)`
	faWeek = `days?(?:\s+(?:a|per)\s+|\s*/\s*)week` // Outtake: "5 days/week"

	faPythonInAList = `\bpython\s*,|,\s*python\b`
	// faResideAsk asks where the applicant lives after an attendance
	// preamble (OneTrust: "This is a hybrid position out of our Atlanta
	// office. Do you currently reside in Atlanta?"): a residence question.
	faResideAsk = `\b(?:do|are)\s+you\s+(?:currently\s+)?(?:reside|live|living|residing|located|based)\s+(?:in|near|within)\b`

	// faSkillGateAnti is the veto set of the "do you have" skill gate.
	faSkillGateAnti = `(?:` + guardQuantifiedExperience + `|` + guardSuperlativePickOne + `|` + guardListChoicePrompt + `)`
	// faWorkplaceAsk asks about a kind of employer or a place of work (a
	// startup, a government, an agency, "working at Acme"), which the
	// disclosure and employer-history entries answer; "working at scale" is a
	// skill.
	faWorkplaceAsk = `\bstart-?ups?\b|\bgovern\w*|\bagenc(?:y|ies)\b|\bwork(?:ed|ing)?\s+(?:for\b|at\s+(?:[^s]|s[^c]))`
	// faSkillHaveAnti is the veto set of the "have experience" gate; like the
	// "do you have" gate it never answers a "which one" ask with Yes.
	faSkillHaveAnti = `(?:` + guardQuantifiedExperience + `|` + guardSuperlativePickOne + `|` + guardListChoicePrompt + `)`

	faQualifier  = `(?:(?:professional|relevant|total|industry)\s+)?`
	faDiscipline = `(?:software engineering|software development|product development|programming|engineering|technical)`
	// faHardTech ends each word at a boundary; c++ and c# end at their last
	// symbol, so they match at the end of the text and before punctuation.
	faHardTech = `(?:(?:python|sql|java(?:script)?|typescript|node\.?js|react|angular|vue|go|golang|rust|scala|kotlin|swift|ruby|php|r|matlab|hadoop|spark|kafka|aws|azure|gcp|kubernetes|docker|terraform|git|linux|bash|shell|ml|machine learning|deep learning|llm|fine[- ]?tune|rag|nlp|pytorch|tensorflow|scikit|pandas|numpy|sqlite|postgres|mysql|mongodb|redis|elasticsearch|tableau|power bi|excel|dbt|airflow|databricks|jira|confluence|pagerduty|opsgenie|sentry|kibana|grafana|datadog|splunk|prometheus|new relic|github|gitlab|jenkins)\b|c\+\+|c#)`
	// faAIToolNameInContext matches an assistant's name (cursor, codex, aider)
	// only as a whole word next to a word that makes it an AI coding tool.
	faAIToolNameInContext = `\b(?:use|using|used|prefer|ai|llm|assistants?|copilots?|tools?|ide|editor|coding)\b[^.?!]*\b(?:cursor|codex|aider)\b|\b(?:cursor|codex|aider)\b[^.?!]*\b(?:ai|llm|assistants?|copilots?|tools?|ide|editor|coding|daily)\b|^\s*(?:cursor|codex|aider)\s*\??\s*$`
	faModelTerm           = `(?:large language models?|llms?|ai models?|neural net\w*|transformers?|bert|gpt|llama|mistral|falcon|claude)`
)

var (
	faAISpecificRE = lazyregexp.New(`(?i)\bai[- ]specific\s+technolog`)
	faComfortRE    = lazyregexp.New(`(?i)\bcomfortable\b`)
	// aiCompanyNameRE is a company whose name ends in "AI" after a preposition
	// ("at Scale AI", "with the Fireworks AI team"). The name word is
	// capitalised, so the match keeps the text's case; aiKindRE keeps a kind
	// of AI ("with Generative AI") out of it.
	aiCompanyNameRE = lazyregexp.New(`\b(?i:at|with|to|for|by|join|joining)\s+(?:the\s+)?[A-Z][\w&.-]*\s+AI\b`)
	aiKindRE        = lazyregexp.New(`(?i)\b(?:generative|gen|conversational|agentic|applied|responsible|using|any|of|an)\s+ai$`)
	aiWordRE        = lazyregexp.New(`(?i)\b(?:ai|artificial\s+intelligence)\b`)
)

// namesAI reports artificial intelligence named outside a company's name.
func namesAI(text string) bool {
	return aiWordRE.MatchString(aiCompanyNameRE.ReplaceAllStringFunc(text, func(m string) string {
		if aiKindRE.MatchString(m) {
			return m
		}
		return " "
	}))
}

func faAISpecificOrComfort(text string) bool {
	return faAISpecificRE.MatchString(text) || faComfortRE.MatchString(text)
}

func fixedRoutesA() []Route {
	var all []Route
	for _, part := range [][]Route{
		fixedASkills(),
		fixedAGates(),
		fixedAWotc(),
		fixedAOffice(),
		fixedAConsent(),
		fixedAConflict(),
	} {
		all = append(all, part...)
	}
	return all
}

func fixedASkills() []Route {
	return []Route{
		{Priority: 1, Target: "preferred-tech-stack",
			Pattern: `\b(?:preferred|favou?rite)\b[^?]{0,40}\btech(?:nology)?\s+stack\b|\btech(?:nology)?\s+stack\b[^?]{0,60}\bprefer`},
		{Priority: 2, Target: "ai-technologies-comfort",
			Pattern: `\bai[- ]specific\s+technolog\w*|\b(?:ai|ml)\b[^?]*\btechnolog\w*\b`,
			// The AI/ML shape needs a comfort word on either side of the nouns.
			Require: faAISpecificOrComfort},
		{Priority: 43, Target: "programming-languages-used",
			Pattern: `\b(?:list|name|tell us|share|provide|which|what)\b[^.?!]{0,80}\b(?:programming|coding|scripting)\s+languages?\b|\b(?:programming|coding|scripting)\s+languages?\b[^.?!]*\b(?:you\s+(?:have|know|are|use|used)|experience|familiar|proficient|worked)\b`,
			Exclude: `\b(?:preferred|prefer|interview(?:ing)?|technical screen|pair program\w*)\b|` + pickStrongestCodingLanguage},
		{Priority: 168, Target: "practical-technical-assessment",
			Pattern: `\b(?:comfortable|willing|able|open)\b[^?!]*\b(?:complete|completing|take|taking|do|doing)\b[^?!]*\b(?:practical|technical|take[- ]home)\b[^?!]*\b(?:assessment|assignment|test|exercise|challenge|task)s?\b`},
		{Priority: 194, Target: "skill-experience-knowledge-gate",
			Pattern: `\bdo you have the required\b.*\bneeded for (?:this|the) (?:job|role|position)\b`},
		{Priority: 255, Target: "no-ai-during-assessment-attestation",
			Pattern: `\bnot\s+(?:to\s+)?(?:use|utili[sz]e|employ)\b[\s\S]{0,80}?\b(?:artificial intelligence|ai)\b[\s\S]{0,400}?\b(?:assessment|interview|screening)\b`},
		{Priority: 256, Target: "no-ai-during-assessment-attestation",
			Pattern: `\b(?:assessment|interview|screening)\b[\s\S]{0,400}?\bnot\s+(?:to\s+)?(?:use|utili[sz]e|employ)\b[\s\S]{0,80}?\b(?:artificial intelligence|ai)\b`},
		{Priority: 257, Target: "ai-future-jobs-consent", Require: namesAI,
			Pattern: `\b(?:artificial intelligence|ai)\b.*\b(?:tool\w*|matching|process\w*|consideration)\b.*\b(?:future|upcoming|other)\s+(?:job|role|opening|position)s?\b`},
		{Priority: 258, Target: "ai-future-jobs-consent", Require: namesAI,
			Pattern: `\b(?:permit|consent|allow|authori[sz]e)\w*\b.*\b(?:process\w*|use)\b.*\b(?:data|application)\b.*\b(?:artificial intelligence|ai)\b`},
		{Priority: 259, Target: "ai-future-jobs-consent", Require: namesAI,
			Pattern: `\b(?:future|other|upcoming)\s+(?:job|role|opening|position)s?\b.*\b(?:artificial intelligence|ai)\b`},
		{Priority: 261, Target: "ai-tasks-performed",
			Pattern: `\bai[- ]related\s+tasks?\b`,
			Exclude: `\b(?:training|train)\b.{0,40}\bfine[- ]?tun\w*|\bfine[- ]?tun\w*|\bnone of the above\b`},
		// Python in a comma list of languages ("(Python, Java, Go)") asks the
		// years of them all, not Python's.
		{Priority: 279, Target: "years-experience-python",
			Pattern: `\bpython\b.*\b(?:years?\s+of\s+(?:(?:work|professional|relevant|total)\s+)?experience|how many years)\b`,
			Exclude: guardNarrativeExample + `|` + faPythonInAList},
		{Priority: 280, Target: "years-experience-python",
			Pattern: `\b(?:years?\s+of\s+(?:(?:work|professional|relevant|total)\s+)?experience|how many years)\b.*\bpython\b`,
			Exclude: guardNarrativeExample + `|` + faPythonInAList},
		{Priority: 281, Target: "years-full-time-work-experience-number",
			Pattern: `\bnumber of years\b[^?]*\bwork experience\b`,
			Exclude: `\bwork experience\b.*\bwith\b`, Veto: roleYears},
		// The second form is "relevant, post college work experience": the total
		// of the applicant's full-time years once schooling is set aside. The
		// qualifier after a comma is why 287 does not reach it.
		{Priority: 282, Target: "years-full-time-work-experience-number",
			Pattern: `^\s*years? of experience\s*[:*]?\s*$|\bhow many years of (?:(?:professional|relevant|total|full[- ]time),?\s+)*post[- ]?(?:college|graduation|graduate|degree|university|school)\s+(?:full[- ]time\s+)?work experience\b`,
			Exclude: `\bwork experience\b.*\bwith\b`},
		// Years in a kind of role (BigID: "in a customer-facing technical
		// role"; "as a financial analyst") or in a subject (n8n: "do you have
		// in sales or as a sales development representative") are that role's
		// or subject's, not the career's, on every total-years route.
		{Priority: 284, Target: "years-experience-total",
			Pattern: `\bhow many years of ` + faQualifier + `experience\b`, Veto: func(t string) bool { return roleYears(t) || inYearsSubject(t) != "" }},
		// Glean: "the total years of experience you have that are relevant for
		// this role"; a skill after the experience is the skill's.
		{Priority: 285, Target: "years-experience-total",
			Pattern: `\byears of (?:professional|relevant|total)\s+experience\b|\btotal\s+years\s+of\s+(?:\w+\s+)?experience(?:\s+(?:you\s+have|that|which)\b|\W*$)`, Veto: roleYears},
		// Modern Health: "years of relevant professional software development
		// experience", the qualifiers before the discipline.
		{Priority: 286, Target: "years-experience-total",
			Pattern: `\byears of (?:(?:professional|relevant|total)\s+)*` + faDiscipline + `(?:\s*(?:/|,|,?\s*and|,?\s*or)\s*` + faDiscipline + `)*\s+experience\b`},
		{Priority: 287, Target: "years-experience-total",
			Pattern: `\bhow many years of ` + faQualifier + `work(?:ing)? experience\b`,
			Exclude: `\bwork(?:ing)? experience\b.*\bwith\b`, Veto: roleYears},
		{Priority: 288, Target: "years-experience-total",
			Pattern: `\byears of ` + faQualifier + `work(?:ing)? experience\b`,
			Exclude: `\bwork(?:ing)? experience\b.*\bwith\b`, Veto: roleYears},
	}
}

func fixedAGates() []Route {
	return []Route{
		// LiveKit: "Do you contribute to open source repos?" asks the
		// applicant's own record as an experience gate does. ClickHouse: "Do
		// you have a strong understanding of database fundamentals: SQL, ...?"
		// asks a knowledge; Energy Solutions: "Do you have intermediate
		// proficiency using spreadsheets (such as Microsoft Excel) ...?".
		{Priority: 289, Target: "skill-experience-knowledge-gate",
			Pattern: `\bdo you have\b.*\b(?:knowledge|understanding|experience|proficiency|front[- ]?end|mobile\s+(?:web|app)\w*|api frameworks?|javascript|node\.?js|html|css|responsive|adaptive|python|source control|git|aws|ec2|lambda|s3|dynamodb|sqs|go|golang)\b|` +
				`\b(?:do you\s+(?:actively\s+|regularly\s+)?contribute|have you\s+(?:ever\s+)?contributed)\s+to\b[^?]*\bopen[- ]?source\b`,
			// "Do you have an AWS certification?" asks a credential, which the
			// certifications list answers.
			Exclude: faSkillGateAnti + `|\bcertif(?:ications?|icates?)\b|` + faWorkplaceAsk},
		{Priority: 290, Target: "skill-experience-knowledge-gate",
			Pattern: `\b(?:have|has)\b.*\b(?:experience|knowledge)\b.*\b(?:web|mobile|front[- ]?end|api|javascript|node\.?js|html|css|python|git|aws|go|golang)\b`,
			Exclude: faSkillHaveAnti + `|` + faWorkplaceAsk},
		{Priority: 291, Target: "skill-experience-knowledge-gate",
			Pattern: `\bare you\s+(?:proficient|experienced|skilled|familiar)\b.*\b` + faHardTech,
			Exclude: guardListChoicePrompt},
		{Priority: 292, Target: "skill-experience-knowledge-gate",
			Pattern: `\b(?:fine[- ]?tun(?:e|ed|ing)|train(?:ed|ing)?)\b.*\b` + faModelTerm + `\b|\bllms?\b.*\b(?:fine[- ]?tun\w*|train\w*|rag|vector (?:db|database|store))\b`,
			// A consent to train models on the applicant's data asks no skill.
			Exclude: guardListChoicePrompt + `|\b(?:consent|permit|allow|authori[sz]e|agree)\w*\b`},
		// Vibe: "Have you written TypeScript on the backend in production?" asks
		// the applicant's record with a technology as the gates above do; a use
		// on the application itself is a statement about it.
		{Priority: 637, Target: "skill-experience-knowledge-gate",
			Pattern: `^\W*have\s+you\s+(?:ever\s+)?(?:written|used|built|shipped|developed|deployed|worked\s+with|programmed|coded)\b[^?]*?\b` + faHardTech,
			Exclude: faSkillHaveAnti + `|` + faWorkplaceAsk + `|\b(?:application|this\s+form|answers?|responses?|cover\s+letter|r[e\x{00E9}]sum[e\x{00E9}])\b`},
		{Priority: 487, Target: "edu-experience-gate",
			Pattern: `\b(?:high school|required education|required work experience|do you possess the required)\b`},
		{Priority: 519, Target: "ai-usage-ok",
			Pattern: `\b(?:comfortable|ok|okay)\b.*\bai usage\b|\bai usage is (?:a )?requirement\b|\b(?:comfortable|ok|okay)\b.*\b(?:environment|work)\b.*\b(?:ai usage|ai use|ai is (?:a )?requirement|integrated)\b`},
		{Priority: 534, Target: "ai-coding-tools-daily-driver",
			Pattern: `\b(?:ai coding tools?|coding tools?|claude code|daily driver)\b|` + faAIToolNameInContext,
			Exclude: `\bmouse\s+cursor\b|\bcursor\s+(?:position|pointer|movement)\b`},
		{Priority: 535, Target: "built-ai-ml-project",
			Pattern: `\b(?:built|build|create|created|made|develop|developed|worked on|done)\b[^.?!]*\b(?:llms?|large language models?|generative ai|ml model|machine learning|deep learning|neural network)s?\b`},
		{Priority: 544, Target: "apis-frameworks-experience", Pattern: `\bbuilding apis?\b`},
		{Priority: 545, Target: "apis-frameworks-experience", Pattern: `\bapis? and (?:working with )?frameworks?\b`},
		// A cloud certification held is the certifications list's (route 627).
		{Priority: 546, Target: "cloud-experience", Pattern: `\b(?:aws|azure|gcp|google cloud)\b`, Exclude: `\bcertifi(?:ed|cations?|cates?)\b`},
		{Priority: 547, Target: "cloud-experience", Pattern: `\bcloud (?:environments?|platforms?|services?|providers?|computing)\b`},
		{Priority: 548, Target: "docker-kubernetes-experience", Pattern: `\b(?:docker|kubernetes|k8s)\b`},
		{Priority: 549, Target: "docker-kubernetes-experience", Pattern: `\bcontainer(?:s|ization)?\b`},
		{Priority: 550, Target: "python-frameworks-experience", Pattern: `\b(?:django|flask|fastapi)\b`},
	}
}

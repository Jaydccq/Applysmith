package interview

// baseQuestions is the fixed question set of a stage type.
func baseQuestions(stageType string) []Question {
	q := func(category, prompt string) Question {
		return Question{Category: category, Prompt: prompt, StoryIDs: []string{}, Sources: []string{}}
	}
	switch stageType {
	case TypeBehavioral, TypeFinal:
		return []Question{
			q("story", "Tell me about a project you are proud of and what you personally did."),
			q("motivation", "Why this company and this role, and why now?"),
			q("trade-off", "Describe a decision where you had to trade one goal for another. What did you choose and why?"),
		}
	case TypeSystemDesign:
		return []Question{
			q("architecture", "Design a service in this role's domain. Start from the requirements, then walk through the main components."),
			q("trade-off", "Which trade-offs in your design would you revisit as load grows, and why?"),
			q("failure mode", "How would your design fail, and how would you detect and recover from it?"),
		}
	case TypeTechnical:
		return []Question{
			q("depth", "Explain in depth a technical area from this role's stack that you know best."),
			q("debugging", "Walk me through how you would debug a production problem in a system you own."),
			q("collaboration", "Tell me about a time you worked through a technical disagreement with a colleague."),
		}
	}
	return []Question{
		q("role fit", "Why are you a fit for this role? Walk me through your most relevant experience."),
		q("evidence", "What is the strongest evidence from your past work for the top requirement of this role?"),
	}
}

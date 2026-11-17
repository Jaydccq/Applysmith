package match

func routeCasesFixedAGates() map[int]ruleCases {
	return map[int]ruleCases{
		289: {
			Pos: []string{"Do you have experience with Python?", "Do you have front-end knowledge?", "Do you contribute to open source repos?", "Have you ever contributed to open-source projects?",
				// ClickHouse (Ashby, queue164).
				"Do you have a strong understanding of database fundamentals: SQL, data modeling, query optimization, and familiarity with OLAP/analytical databases?"},
			Neg: []string{
				"Do you have 5+ years of Python experience?", "Do you have an AWS certification?", "Do you have coding samples?",
				"Which cloud platform do you have the most experience with?",
				"Which of the following tools do you have experience with? Select all that apply.",
				"Do you have a pet?",
				"Do you have experience working at a startup?", "Do you have government employment experience?", "Do you have previous experience working at Acme?",
			},
		},
		290: {
			Pos: []string{"Do you have experience building web applications?", "The candidate has knowledge of git"},
			Neg: []string{"Which one do you have the most experience with: aws or azure?", "Do you have 3+ years of experience with Python?", "Which of the following do you have experience with in git? Select all that apply", "Do you have experience with Fortran?",
				"Has the candidate experience working for a government agency on web projects?"},
		},
		291: {
			Pos: []string{"Are you proficient in Python?", "Are you experienced with Kubernetes?", "Are you familiar with Example Corp tooling and sql?", "Are you proficient in C++11?", "Are you proficient in C++?", "Are you familiar with tools such as Confluence, Jira, and on-call management software such as PagerDuty and experience with error monitoring software (Sentry, Kibana)?", "Are you experienced with Datadog or Grafana?"},
			Neg: []string{"Are you proficient in Spanish?", "Which of the following are you proficient in? Python, Java"},
		},
		292: {
			Pos: []string{"Have you fine-tuned large language models?", "Have you trained a neural network?", "Do you use LLMs with RAG?"},
			Neg: []string{"Have you trained for a marathon?", "Which of the following have you trained: LLMs, transformers?", "Do you like machine learning?",
				"Do you consent to your data being used to train AI models?"},
		},
		637: {
			Pos: []string{"Have you written TypeScript on the backend in production?", "Have you used Python in production?", "Have you built production systems in Go?"},
			Neg: []string{"Have you used an LLM to write your application answers?", "Which of the following have you used: Python, Java?", "Have you worked at a startup using React?"},
		},
		487: {
			Pos: []string{"Do you have a high school diploma?", "Do you possess the required education?", "Required work experience"},
			Neg: []string{"Do you like school lunches?", "What is your degree?"},
		},
		519: {
			Pos: []string{"Are you comfortable working in an environment where AI usage is encouraged?", "Are you ok with AI usage?", "AI usage is a requirement for this role. Are you comfortable?", "Are you okay with a work environment where AI is integrated?"},
			Neg: []string{"Are you comfortable with relocation?", "Which AI tools do you use?"},
		},
		534: {
			Pos: []string{"Which AI coding tools do you use?", "Is Claude Code your daily driver?", "Do you use Cursor?"},
			Neg: []string{"Which hobbies do you have?", "Tell us about your favorite tools"},
		},
		535: {
			Pos: []string{"Have you built a project using LLMs?", "Have you developed a machine learning model?", "Have you created a generative AI application?"},
			Neg: []string{"Share links to your projects", "Have you built a mobile app?", "How many years have you used machine learning?"},
		},
		544: {
			Pos: []string{"Do you have experience building APIs?"},
			Neg: []string{"Do you have experience building houses?"},
		},
		545: {
			Pos: []string{"Experience with APIs and frameworks", "Do you know apis and working with frameworks?"},
			Neg: []string{"Experience with databases and frameworks"},
		},
		546: {
			Pos: []string{"Do you have experience with AWS?", "Experience in Azure or Google Cloud"},
			Neg: []string{"Do you have experience with Docker?"},
		},
		547: {
			Pos: []string{"Experience with cloud platforms", "Do you work with cloud computing?"},
			Neg: []string{"Experience with cloudy weather"},
		},
		548: {
			Pos: []string{"Do you have experience with Docker?", "Experience with Kubernetes or k8s"},
			Neg: []string{"Do you have experience with Terraform?"},
		},
		549: {
			Pos: []string{"Experience with containerization", "Have you run containers in production?"},
			Neg: []string{"Experience with shipping crates"},
		},
		550: {
			Pos: []string{"Experience with Django", "Have you used Flask or FastAPI?"},
			Neg: []string{"Experience with Rails"},
		},
	}
}

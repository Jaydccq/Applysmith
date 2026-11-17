package wire

// ResumeEntry is one entry of the résumé roster. It carries the facts of the
// file and never its bytes.
type ResumeEntry struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Tags        []string `json:"tags"`
	Default     bool     `json:"default"`
	Description string   `json:"description"`
	FileName    string   `json:"fileName"`
	// Size is the file's size in bytes, zero when the file cannot be read.
	Size SafeInt `json:"size"`
}

// ResumeList is the roster in roster order.
type ResumeList struct {
	Resumes []ResumeEntry `json:"resumes"`
}

// ResumeRankRequest is the text of a posting. Description is at most 20,000
// characters; Company is accepted but does not score.
type ResumeRankRequest struct {
	Title       string `json:"title"`
	Company     string `json:"company"`
	Description string `json:"description"`
}

// ResumeRankItem is one roster entry's place in a ranking. Reason lists the
// matched posting words, or is "default" for the chosen entry when none matched.
type ResumeRankItem struct {
	ID     string  `json:"id"`
	Score  SafeInt `json:"score"`
	Reason string  `json:"reason"`
}

// ResumeRank lists every roster entry best first and names the chosen one.
type ResumeRank struct {
	Chosen  string           `json:"chosen"`
	Ranking []ResumeRankItem `json:"ranking"`
}

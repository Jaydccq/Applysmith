package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/research"
)

// Researcher answers an open question from the web with numbered sources.
type Researcher interface {
	Research(ctx context.Context, req research.Request) (research.Answer, error)
}

// noSearchSource is the whole answer when no web search is available: nothing
// was asked of a model, and the text names the two ways to get one. The route
// is chosen where llm.NoModel says; the Exa key has no control on the
// dashboard, so its command stays.
const noSearchSource = "Not researched: no web-search source is configured, so no model was asked. " +
	"Route the research-search task to OpenRouter under Settings, Models, whose built-in web search is then used, " +
	"or store an Exa key (`applysmith secrets set api-key-exa`)."

func researchQuestion(r Researcher) Tool {
	return Tool{Name: "research_question", Label: "Researching on the web", Class: ClassDraft, OutsideContent: true, PII: PIILow, Timeout: research.Deadline + 5*time.Second,
		Description: "Search the web to answer an open question about the outside world: a company's news, funding or products, an industry, the latest version of a technology, a public fact that changes. " +
			"The answer rests only on the pages the search returned, every line cites a numbered source, and the sources come back with title and URL; a reply that cannot be tied to its sources is refused, not shown. " +
			"Use it when the user asks you to research or look up something online, or asks a question their own records cannot answer. Do not use it for the user's tracker, reports, profile or knowledge base (use their tools), to find job postings (list_pipeline lists the postings the scans found and scan_portal starts a scan; a web search finds aggregator copies with unverified dates and taints the turn, which holds the scan), or to read one given page (use read_page). " +
			"It takes up to 45 seconds, stores nothing and sends nothing; the pages are third-party text and taint the turn. Pass the company or the posting URL when the question is about one.",
		Schema: `{"type":"object","additionalProperties":false,"required":["question"],"properties":{
			"question":{"type":"string","minLength":2,"maxLength":2000},
			"company":{"type":"string","maxLength":200},
			"job_url":{"type":"string","maxLength":2000,"description":"The posting the question comes from; it is context and is not fetched."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Question, Company string
				JobURL            string `json:"job_url"`
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			ans, err := r.Research(ctx, research.Request{Question: a.Question, Company: a.Company, JobURL: a.JobURL})
			var (
				uncited *research.UncitedError
				late    *research.TimeoutError
				failed  *research.SearchError
				model   *llm.Error
			)
			switch {
			case err == nil:
			case errors.Is(err, research.ErrNoSource):
				return ToolResult{Text: noSearchSource}, nil
			case errors.Is(err, research.ErrInvalid):
				return ToolResult{Text: "Not researched: " + strings.TrimPrefix(err.Error(), "research: ") + "."}, nil
			case errors.Is(err, research.ErrNoResults):
				return ToolResult{Text: "Not answered: the web search returned no sources, so there is nothing to cite."}, nil
			case errors.As(err, &late):
				text := "Not answered: the web search took too long (over " + late.After.Round(time.Second).String() + "), so nothing is claimed. Ask again, or narrow the question."
				if len(late.Sources) > 0 {
					text += "\nPages the search had already found (read them or ask again):\n" + sourceList(late.Sources)
				}
				return ToolResult{Text: text, Limitations: []string{"The web search did not finish in time, so no cited answer was produced."}}, nil
			case errors.As(err, &failed):
				return ToolResult{Text: "Not answered: the web search failed (" + oneLine(failed.Err.Error()) + "). No model was asked, so nothing is claimed.",
					Limitations: []string{"The web search failed, so no cited answer was produced."}}, nil
			case errors.As(err, &uncited):
				return ToolResult{Text: "Not answered: the model's reply was refused because " + uncited.Reason + ". It is not shown. " +
					"Pages the search found (read them or ask again):\n" + sourceList(uncited.Sources),
					Limitations: []string{"The search ran, but no cited answer came out of it."}}, nil
			case errors.Is(err, llm.ErrNoRoute):
				return ToolResult{Text: "Not researched: " + llm.NoModel("the "+research.Task+" task") + "."}, nil
			case errors.As(err, &model) && ctx.Err() == nil:
				return ToolResult{Text: "Not answered: the model call of the research-search task failed (" + oneLine(model.Error()) + "). Check it with `applysmith llm test`.",
					Limitations: []string{"The model call failed, so no cited answer was produced."}}, nil
			default:
				return ToolResult{}, err
			}
			if ans.Unanswered {
				return ToolResult{Text: "The web sources found do not answer the question, so nothing is claimed. Pages found:\n" + sourceList(ans.Sources),
					Limitations: []string{"The web sources found do not answer the question."}}, nil
			}
			out := ToolResult{
				Text: fmt.Sprintf("Answer (from %d of %d web sources found by %s; the sources are third-party pages):\n%s\n\nSources:\n%s",
					len(ans.Sources), ans.Found, ans.Via, ans.Text, sourceList(ans.Sources)),
				Limitations: []string{"Rests only on the web search results listed as sources; nothing else was checked."},
			}
			for _, s := range ans.Sources {
				// The call's own numbers mean nothing next to another call's, so the
				// citation carries the title and the link, not the number.
				out.Citations = append(out.Citations, Citation{Label: cmp.Or(s.Title, s.URL), URL: s.URL})
			}
			return out, nil
		}}
}

func sourceName(s research.Source) string {
	if s.Title == "" {
		return s.URL
	}
	return s.Title + " (" + s.URL + ")"
}

func sourceList(sources []research.Source) string {
	lines := make([]string, len(sources))
	for i, s := range sources {
		lines[i] = fmt.Sprintf("[%d] %s", s.N, sourceName(s))
	}
	return strings.Join(lines, "\n")
}

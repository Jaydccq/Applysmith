package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"applysmith/internal/agent/memory"
	"applysmith/internal/kb"
)

// WikiEditor is the door the Ask Agent maintains the wiki through: the
// knowledge service, which validates the frontmatter it owns, scans for
// secrets, lints and refuses an edit made against a file that changed since it
// was read. The model supplies the user's words only.
type WikiEditor interface {
	Find(ctx context.Context, q string) ([]kb.Entry, error)
	Show(ctx context.Context, id string) (kb.Document, error)
	PreviousVersion(ctx context.Context, id string) (kb.Previous, error)
	CreateSnippet(ctx context.Context, in kb.SnippetInput) (kb.Published, error)
	Revise(ctx context.Context, id string, r kb.Revision, expectedHash string) (kb.Document, error)
}

const (
	// wikiFound is how many pages wiki_find lists.
	wikiFound = 8
	// wikiReadRunes is the longest page wiki_read returns. A page the model
	// cannot read whole is not rewritten from a cut copy.
	wikiReadRunes = 10000
	// wikiBodyRunes bounds the body of one wiki_write so that the approval
	// card, which shows the call's arguments cut at 4,000 bytes, shows all of it.
	wikiBodyRunes = 3000
	// wikiCardBytes is the most argument bytes a call may carry for the same
	// reason; a longer call is refused and writes nothing.
	wikiCardBytes = 3800
)

func wikiFind(w WikiEditor) Tool {
	return Tool{Name: "wiki_find", Label: "Looking through the wiki", Class: ClassRead, PII: PIILow, Timeout: readTimeout,
		Description: "List the user's wiki pages whose title, tags or text hold every word of the query: each page's id, category, title, a one-line description, the link path other pages use and the file hash. " +
			"Use it before writing, to find the page a new statement belongs to. wiki_read gives a page's text.",
		Schema: `{"type":"object","additionalProperties":false,"required":["query"],"properties":{
			"query":{"type":"string","minLength":2,"maxLength":200}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Query string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			found, err := w.Find(ctx, a.Query)
			if err != nil {
				return ToolResult{}, err
			}
			var pages []kb.Entry
			for _, e := range found {
				if e.Kind == kb.KindSnippet {
					pages = append(pages, e)
				}
			}
			if len(pages) == 0 {
				return ToolResult{Text: "No wiki page holds all the words of that. A page is found by its text: try fewer or other words."}, nil
			}
			var s strings.Builder
			for i, e := range pages {
				if i == wikiFound {
					fmt.Fprintf(&s, "and %d more; a narrower query lists them.\n", len(pages)-wikiFound)
					break
				}
				fmt.Fprintf(&s, "- %s | %s | %s | %s | link %s | hash %s\n", e.ID, e.Category, e.Title, cmp.Or(cutRunes(oneLine(e.Description), 160), "no description"), wikiLink(e.Path), e.FileHash)
			}
			return ToolResult{Text: strings.TrimSpace(s.String())}, nil
		}}
}

// wikiLink is the path another page uses to link to the page at path: relative
// to the knowledge directory, with a leading slash.
func wikiLink(path string) string {
	return "/" + strings.TrimPrefix(path, kb.KnowledgeDir+"/")
}

func wikiRead(w WikiEditor) Tool {
	return Tool{Name: "wiki_read", Label: "Reading a wiki page", Class: ClassRead, PII: PIILow, Timeout: readTimeout,
		Description: "Read one wiki page: its text, the statements autofill may state from it, the pages it links to and its file hash, which wiki_write needs to change it. " +
			"With previous true it returns the page as it was before its last edit instead of the current text, which wiki_write puts back as the undo.",
		Schema: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{
			"id":{"type":"string","minLength":1,"maxLength":120},
			"previous":{"type":"boolean"}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				ID       string
				Previous bool
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			doc, err := w.Show(ctx, a.ID)
			if r, ok := wikiRefusal(err); ok {
				return r, nil
			} else if err != nil {
				return ToolResult{}, err
			}
			sn, err := kb.ParseSnippet(doc.Path, []byte(doc.Markdown))
			if err != nil || doc.Kind != kb.KindSnippet {
				return ToolResult{Text: fmt.Sprintf("%q is not a wiki page that can be read here.", a.ID)}, nil
			}
			title, body := doc.Title, doc.Body
			var head string
			if a.Previous {
				prev, err := w.PreviousVersion(ctx, a.ID)
				if r, ok := wikiRefusal(err); ok {
					return r, nil
				} else if err != nil {
					return ToolResult{}, err
				}
				title, body = prev.Title, prev.Body
				if old, err := kb.ParseSnippet(doc.Path, []byte(prev.Markdown)); err == nil {
					sn = old
				}
				head = fmt.Sprintf("Version before the last edit (replaced at %s). The page has hash %s now\n", prev.SavedAt, doc.FileHash)
			}
			if utf8.RuneCountInString(body) > wikiReadRunes {
				return ToolResult{Text: fmt.Sprintf("%q is too long to rewrite from a chat (%d characters); the user edits it on the Wiki page.", a.ID, utf8.RuneCountInString(body))}, nil
			}
			var s strings.Builder
			s.WriteString(head)
			fmt.Fprintf(&s, "Page %s | %s | %s | hash %s\n", doc.ID, doc.Category, title, doc.FileHash)
			var links []string
			for _, l := range sn.Links {
				links = append(links, l.Path)
			}
			fmt.Fprintf(&s, "Links to: %s\n", cmp.Or(strings.Join(links, ", "), "none"))
			if facts := sn.Facts(); len(facts) > 0 {
				fmt.Fprintf(&s, "Allowed facts: %s\n", strings.Join(facts, " | "))
			}
			s.WriteString("---\n" + body)
			return ToolResult{Text: s.String()}, nil
		}}
}

func wikiWrite(w WikiEditor) Tool {
	return Tool{Name: "wiki_write", Label: "Saving a wiki page", Class: ClassWriteLocal, RequiresApproval: true, PII: PIILow, Timeout: readTimeout,
		Description: "Create a wiki page, or change one, from what the user told you. The page is published at once and autofill draws on it from then on. " +
			"Without id this creates a page: give category, title and markdown. With id it changes that page: give expected_hash, the hash wiki_read returned, and the markdown (the whole body, not a part of it) and/or a title. " +
			"A hash that no longer matches the page is refused; read the page again and redo the change. facts: short statements the user made, added to the page's allowed facts. " +
			"The body is the page's Markdown without its frontmatter; link a related page as [label](/projects/id.md). The user approves the exact call on a card first.",
		Schema: fmt.Sprintf(`{"type":"object","additionalProperties":false,"properties":{
			"id":{"type":"string","maxLength":120},
			"expected_hash":{"type":"string","maxLength":64},
			"category":{"type":"string","enum":["projects","experience","answer-patterns"]},
			"title":{"type":"string","minLength":1,"maxLength":120},
			"markdown":{"type":"string","minLength":1,"maxLength":%d},
			"facts":{"type":"array","maxItems":8,"items":{"type":"string","minLength":2,"maxLength":300}}}}`, wikiBodyRunes),
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				ID, Category, Title, Markdown string
				ExpectedHash                  string `json:"expected_hash"`
				Facts                         []string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			refuse := func(format string, args ...any) (ToolResult, error) {
				return ToolResult{Text: "Not changed: " + fmt.Sprintf(format, args...)}, nil
			}
			// The guard chain's approval requirement ran before this point, so
			// a call that reaches the tool carries the user's approval.
			if len(c.Args) > wikiCardBytes {
				return refuse("the call is too long for the approval card to show in full; write a shorter page, or split it in two")
			}
			for _, text := range append([]string{a.Title, a.Markdown}, a.Facts...) {
				if memory.InjectionShaped(text) {
					return refuse("the text reads as an instruction to the assistant, so nothing was stored")
				}
			}
			if strings.TrimSpace(a.ID) == "" {
				return wikiCreate(ctx, w, a.Category, a.Title, a.Markdown, a.Facts)
			}
			return wikiUpdate(ctx, w, a.ID, a.ExpectedHash, a.Title, a.Markdown, a.Facts)
		}}
}

func wikiCreate(ctx context.Context, w WikiEditor, category, title, markdown string, facts []string) (ToolResult, error) {
	if category == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(markdown) == "" {
		return ToolResult{Text: "Not changed: a new page needs a category, a title and markdown."}, nil
	}
	pub, err := w.CreateSnippet(ctx, kb.SnippetInput{Title: title, Category: category, Body: markdown, Facts: facts})
	if r, ok := wikiRefusal(err); ok {
		return r, nil
	} else if err != nil {
		return ToolResult{}, err
	}
	text := fmt.Sprintf("Created the page %q (id %s, %s) and published it: autofill draws on it from now on. To remove it, the user deletes it on the Wiki page; a delete can be undone for 15 minutes.", strings.TrimSpace(title), pub.SnippetID, category)
	return ToolResult{Text: text + lintText(pub.Lint)}, nil
}

func wikiUpdate(ctx context.Context, w WikiEditor, id, hash, title, markdown string, facts []string) (ToolResult, error) {
	switch {
	case strings.TrimSpace(hash) == "":
		return ToolResult{Text: "Not changed: changing a page needs expected_hash, the hash wiki_read returned."}, nil
	case strings.TrimSpace(title) == "" && strings.TrimSpace(markdown) == "" && len(facts) == 0:
		return ToolResult{Text: "Not changed: give the markdown, a title or facts to change."}, nil
	}
	cur, err := w.Show(ctx, id)
	if r, ok := wikiRefusal(err); ok {
		return r, nil
	} else if err != nil {
		return ToolResult{}, err
	}
	if cur.Kind != kb.KindSnippet || cur.SourceType != "" || cur.Category == kb.CatBehavioral {
		return ToolResult{Text: fmt.Sprintf("Not changed: %q is a story or a company brief; the user edits those on the Wiki page.", id)}, nil
	}
	rev := kb.Revision{AddFacts: facts}
	if strings.TrimSpace(title) != "" {
		rev.Title = &title
	}
	if strings.TrimSpace(markdown) != "" {
		rev.Body = &markdown
	}
	doc, err := w.Revise(ctx, id, rev, hash)
	if r, ok := wikiRefusal(err); ok {
		return r, nil
	} else if err != nil {
		return ToolResult{}, err
	}
	text := fmt.Sprintf("Changed the page %q (id %s) and published it: autofill draws on the new text from now on.\nThe page has hash %s now\n"+
		"To undo, wiki_read with previous true shows the text before this change and wiki_write with that text and this hash puts it back; facts added stay until the user removes them on the Wiki page.", doc.Title, doc.ID, doc.FileHash)
	return ToolResult{Text: text + lintText(doc.Lint)}, nil
}

// lintText lists the lint warnings of a saved page; the model passes them on.
func lintText(fs []kb.Finding) string {
	if len(fs) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\nWarnings:")
	for _, f := range fs {
		s.WriteString("\n- " + f.Message)
	}
	return s.String()
}

// wikiRefusal turns a refusal of the knowledge service into the tool's answer.
func wikiRefusal(err error) (ToolResult, bool) {
	var secret *kb.SecretError
	switch {
	case err == nil:
		return ToolResult{}, false
	case errors.As(err, &secret):
		return ToolResult{Text: "Not changed: the text holds what looks like a secret (" + secret.Error() + "). Nothing was stored; leave it out."}, true
	case errors.Is(err, kb.ErrStale):
		return ToolResult{Text: "Not changed: the page changed since it was read. Read it again with wiki_read and redo the change against its current text."}, true
	case errors.Is(err, kb.ErrNotFound):
		return ToolResult{Text: "There is no such page or version. wiki_find lists the pages."}, true
	case errors.Is(err, kb.ErrConflict):
		return ToolResult{Text: "Not changed: " + err.Error() + ". If it is the same topic, read that page with wiki_read and change it instead."}, true
	case errors.Is(err, kb.ErrInvalid):
		return ToolResult{Text: "Not changed: " + err.Error() + "."}, true
	}
	return ToolResult{}, false
}

package discovery

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
)

// Detection kinds.
const (
	DetectBoard      = "board"
	DetectCareersURL = "careers_url"
	DetectName       = "name"
)

// detectBudget bounds the probes of one detection.
const detectBudget = 20 * time.Second

// Candidate is a board that may belong to the company the user typed. Live is
// true when the board answered with at least one posting.
type Candidate struct {
	Board
	Jobs   int
	Live   bool
	Reason string
}

// Detection is what the add-a-company form previews before anything is saved.
type Detection struct {
	Input      string
	Kind       string
	Name       string
	CareersURL string
	Candidates []Candidate
	Note       string
}

var (
	nonAlnum    = lazyregexp.New(`[^a-z0-9]+`)
	legalSuffix = map[string]bool{"inc": true, "llc": true, "ltd": true, "corp": true, "corporation": true, "co": true, "company": true, "gmbh": true, "plc": true}
)

// ErrCareersScheme means a careers address is not https; portals.yml only
// holds https addresses.
var ErrCareersScheme = errors.New("a careers address must be an https URL")

// looksLikeAddress tells a careers address from a company name: a scheme, or
// one word with a dot in it.
func looksLikeAddress(s string) bool {
	return strings.Contains(s, "://") || (!strings.ContainsAny(s, " \t") && strings.Contains(s, "."))
}

// nameSlugs are the board slugs a company name makes: its words joined with
// nothing and with hyphens, with and without a trailing legal suffix.
func nameSlugs(name string) []string {
	words := strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(name), " "))
	var out []string
	add := func(ws []string) {
		for _, s := range []string{strings.Join(ws, ""), strings.Join(ws, "-")} {
			if len(s) >= 2 && ValidSlug(s) {
				out = appendUnique(out, s)
			}
		}
	}
	add(words)
	if n := len(words); n > 1 && legalSuffix[words[n-1]] {
		add(words[:n-1])
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// Detect reads what a company name or a careers address names, without saving
// anything. An address that names a supported board becomes that board; any
// other https address is kept as a careers page. A name is tried as a board
// slug on every supported system, and every board that answers is offered, so
// the user decides which one is theirs.
func (sc *Scanner) Detect(ctx context.Context, input string) (Detection, error) {
	input = strings.TrimSpace(input)
	d := Detection{Input: input, Candidates: []Candidate{}}
	if input == "" {
		return d, errors.New("a company name or careers address is required")
	}
	if !looksLikeAddress(input) {
		d.Kind, d.Name = DetectName, input
		slugs := nameSlugs(input)
		if len(slugs) == 0 {
			d.Note = "The name makes no usable board slug; give a careers address instead."
			return d, nil
		}
		var boards []Board
		for _, slug := range slugs {
			for _, ats := range ATSNames {
				boards = append(boards, Board{ATS: ats, Slug: slug})
			}
		}
		d.Candidates = sc.probe(ctx, boards)
		kept := d.Candidates[:0]
		for _, c := range d.Candidates {
			if c.Live {
				kept = append(kept, c)
			}
		}
		d.Candidates = kept
		if len(kept) == 0 {
			d.Note = "No supported board answered to this name. Give the careers address, or add the company from its board address."
		} else {
			d.Note = "These boards answered. A slug can belong to another organisation with a similar name, so check the postings before saving."
		}
		return d, nil
	}
	raw := input
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return d, errors.New("this is not a web address")
	}
	if board, err := ResolveBoard(raw); err == nil {
		d.Kind = DetectBoard
		d.Candidates = sc.probe(ctx, []Board{board})
		d.Name = d.Candidates[0].company()
		return d, nil
	}
	if u.Scheme != "https" {
		return d, ErrCareersScheme
	}
	d.Kind, d.CareersURL = DetectCareersURL, u.String()
	d.Note = "No supported board matches this address. It will be kept as a careers page and read through careers-page expansion."
	return d, nil
}

// probe fetches each board once, a few at a time, and keeps the input order.
func (sc *Scanner) probe(ctx context.Context, boards []Board) []Candidate {
	ctx, cancel := context.WithTimeout(ctx, detectBudget)
	defer cancel()
	base := sc.Client
	if base == nil {
		base = NewClient()
	}
	client := base.Session(4)
	out := make([]Candidate, len(boards))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, b := range boards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c := Candidate{Board: b}
			got, err := client.FetchBoard(ctx, sc.Endpoints, b, "")
			var fail *BoardFailure
			switch {
			case err == nil:
				c.Live, c.Jobs = true, len(got.Postings)
				for _, p := range got.Postings {
					if p.Company != "" {
						c.Company = p.Company
						break
					}
				}
			case errors.As(err, &fail):
				c.Reason = fail.Reason
			default:
				c.Reason = "the board could not be reached"
			}
			out[i] = c
		}()
	}
	wg.Wait()
	return out
}

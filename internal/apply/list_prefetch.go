package apply

import (
	"context"
	"fmt"
	"sync"
)

// Resolution is what resolving a mirror row found: the employer-side address,
// or the reason none was found.
type Resolution struct {
	URL    string
	Reason string
}

// ResolveFunc resolves one mirror row to the employer's posting. It is a pure
// read: nothing is recorded by it.
type ResolveFunc func(ctx context.Context, c Candidate) (Resolution, error)

// Prefetch holds the mirror resolutions a list started before its walk.
type Prefetch struct {
	futures map[string]*future
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type future struct {
	done chan struct{}
	res  Resolution
	err  error
}

// StartPrefetch starts resolving every candidate now, at most parallel at a
// time and in list order, so the first row's wait is the only one the walk
// pays. A failure is kept with its row and surfaces when the walk reaches it;
// a row the walk never reaches costs nothing. Stop releases the workers.
func StartPrefetch(ctx context.Context, cands []Candidate, resolve ResolveFunc, parallel int) *Prefetch {
	ctx, cancel := context.WithCancel(ctx)
	p := &Prefetch{futures: make(map[string]*future, len(cands)), cancel: cancel}
	for _, c := range cands {
		p.futures[c.URL] = &future{done: make(chan struct{})}
	}
	sem := make(chan struct{}, max(parallel, 1))
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for _, c := range cands {
			f := p.futures[c.URL]
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				f.err = ctx.Err()
				close(f.done)
				continue
			}
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer func() { <-sem }()
				defer close(f.done)
				f.res, f.err = resolve(ctx, c)
			}()
		}
	}()
	return p
}

// Wait returns the resolution started for a URL; ok is false for a URL no
// resolution was started for.
func (p *Prefetch) Wait(ctx context.Context, url string) (res Resolution, ok bool, err error) {
	f, found := p.futures[url]
	if !found {
		return Resolution{}, false, nil
	}
	select {
	case <-f.done:
		return f.res, true, f.err
	case <-ctx.Done():
		return Resolution{}, true, ctx.Err()
	}
}

// Stop cancels the resolutions still running and waits for the workers.
func (p *Prefetch) Stop() {
	p.cancel()
	p.wg.Wait()
}

// prepare is the walk reaching a mirror row: its resolution is awaited, an
// unresolved mirror ends the row with a precheck record written now (so the
// records are in list order and a row an earlier rule skipped writes none),
// and a resolved one is checked against the holds of the employer's address.
func (s *Selector) prepare(ctx context.Context, sel *selection, pf *Prefetch, g MirrorGroup, named bool, row *ListRow, res *SelectResult) (*Skipped, error) {
	c := g.Primary
	if !IsAggregatorURL(c.URL) {
		return nil, nil
	}
	got, ok, err := pf.Wait(ctx, c.URL)
	switch {
	case !ok:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("resolve %s: %w", redactURL(c.URL), err)
	case got.Reason != "":
		s.recordStop(ctx, res, c, got.Reason, ClassPosting, sel.opts)
		return skip(c, RulePrecheck, ownerPost, "%s", got.Reason), nil
	}
	if got.URL != "" && got.URL != c.URL {
		row.EmployerURL = got.URL
		if !named {
			if h, held := sel.holds.reason(got.URL, sel.opts.Build, sel.opts.Now); held {
				return skip(c, RuleHold, h.owner, "%s", h.reason), nil
			}
		}
	}
	return nil, nil
}

// recordStop writes a precheck stop; a failed write is a warning, because the
// row is skipped for this list either way.
func (s *Selector) recordStop(ctx context.Context, res *SelectResult, c Candidate, reason string, class Class, opts SelectOptions) {
	if s.Record == nil {
		return
	}
	err := s.Record.RecordPrecheck(ctx, Precheck{URL: c.URL, Company: c.Company, Role: c.Role, ReportNumber: c.ReportNumber, Reason: reason, Class: class, Build: opts.Build, At: opts.Now})
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the precheck stop for %s was not recorded: %v", redactURL(c.URL), err))
	}
}

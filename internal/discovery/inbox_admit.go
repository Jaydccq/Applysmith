package discovery

import (
	"context"
	"database/sql"
	"fmt"

	"applysmith/internal/workspace"
)

// Admission is the outcome of admitting one posting handed over by another
// capability.
type Admission struct {
	Row *InboxRow
	// Duplicate names the store that already holds the posting, and MatchID
	// the row there.
	Duplicate string
	MatchID   int64
	// Blocked is set when the company is on the blocked list or in
	// skip_companies.
	Blocked bool
	// Invalid is the reason the URL cannot be a posting.
	Invalid string
}

// AdmitPosting admits one posting; a caller with several uses one Admitter.
func (r *Repo) AdmitPosting(ctx context.Context, portals workspace.Portals, profile workspace.Profile, p Posting, detail string) (Admission, error) {
	a, err := r.NewAdmitter(ctx, portals, profile)
	if err != nil {
		return Admission{}, err
	}
	return a.Admit(ctx, p, detail)
}

// Admitter admits postings handed over by another capability through the same
// canonicalization, duplicate detection, blocked-company checks and ranking
// (against the profile's target roles) as a scan row. The title filter is the
// caller's to apply. It reads the blocked list and the known postings once
// and keeps them current with what it inserts, so a batch does not load the
// inbox, the tracker and the scan history for every posting.
type Admitter struct {
	r       *Repo
	filters Filters
	title   TitleRule
	targets RankTargets
	known   *Known
}

// NewAdmitter reads what an admission compares a posting with.
func (r *Repo) NewAdmitter(ctx context.Context, portals workspace.Portals, profile workspace.Profile) (*Admitter, error) {
	blocked, err := r.blockedNames(ctx)
	if err != nil {
		return nil, err
	}
	known, err := r.LoadKnown(ctx)
	if err != nil {
		return nil, err
	}
	return &Admitter{r: r, filters: NewFilters(portals, blocked), title: NewTitleRule(portals.Filters.Title),
		targets: NewRankTargets(profile.Targets.Roles), known: known}, nil
}

// Admit inserts one posting into the inbox, or says why it was not.
func (a *Admitter) Admit(ctx context.Context, p Posting, detail string) (Admission, error) {
	r := a.r
	original := p.URL
	canon, err := postingURL(original)
	if err != nil {
		return Admission{Invalid: err.Error()}, nil
	}
	if NonPostingURL(canon) {
		return Admission{Invalid: FilterNonPostingURL}, nil
	}
	p.URL = canon
	if a.filters.CompanyBlocked(p.Company) {
		return Admission{Blocked: true}, nil
	}
	if store, id, ok := a.known.Match(p, false); ok {
		return Admission{Duplicate: store, MatchID: id}, nil
	}
	now := r.now()
	p.Signals = ComputeSignals(p.Title, p.Description)
	p.Score, p.Reasons = Rank(p, RankInput{Title: a.title.Check(p.Title), Targets: a.targets, Scope: ScopeWorldwide, Now: now})
	var id int64
	var raced bool
	err = r.s.Write(ctx, func(tx *sql.Tx) error {
		var inserted bool
		var err error
		if id, inserted, err = r.insertInbox(ctx, tx, p, original, detail, ""); err != nil {
			return fmt.Errorf("inserting %s: %w", p.URL, err)
		}
		if !inserted {
			raced = true
			return r.historyUpsert(ctx, tx, p, HistoryDuplicate, nil, "")
		}
		score := p.Score
		return r.historyUpsert(ctx, tx, p, HistoryInbox, &score, "")
	})
	if err != nil {
		return Admission{}, err
	}
	if raced {
		return Admission{Duplicate: StoreInbox}, nil
	}
	a.known.addInbox(p, id)
	row, err := r.InboxGet(ctx, id)
	return Admission{Row: &row}, err
}

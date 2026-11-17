package api

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/ledger"
	"applysmith/internal/tracker"
)

const maxSubmissionField = 500

// PressRequest is a posting the extension wants to press the final submit
// control of. The address has no query or fragment.
type PressRequest struct {
	URL, Host, Company, Role string
}

// PressGrant is a live permission to press once for one posting.
type PressGrant struct {
	ID        string
	ExpiresAt time.Time
}

// ErrClaimUnavailable means the duplicate guard or the ledger cannot be
// consulted, which is never a reason to press. ErrUnknownClaim means the claim
// id names no live claim.
var (
	ErrClaimUnavailable = errors.New("press claim unavailable")
	ErrUnknownClaim     = ledger.ErrUnknownClaim
)

// DuplicateSubmitError refuses a claim because the posting is already
// submitted, being submitted, or claimed; Reason is shown to the person.
type DuplicateSubmitError struct{ Reason string }

func (e *DuplicateSubmitError) Error() string { return "duplicate submit blocked: " + e.Reason }

// PressClaimer is the duplicate guard and press claim that the apply-safety
// gates require before any submit. Claim grants one press or returns a
// *DuplicateSubmitError or ErrClaimUnavailable. Settle turns the claim's row
// into the submission the extension reports and returns its ledger id.
type PressClaimer interface {
	Claim(ctx context.Context, req PressRequest) (PressGrant, error)
	Settle(ctx context.Context, claimID string, o ledger.Observation) (ledgerID int64, err error)
}

// unavailableClaimer is the claimer until the apply-safety gates provide one:
// nothing is ever granted, so no press can follow.
type unavailableClaimer struct{}

func (unavailableClaimer) Claim(context.Context, PressRequest) (PressGrant, error) {
	return PressGrant{}, ErrClaimUnavailable
}

func (unavailableClaimer) Settle(context.Context, string, ledger.Observation) (int64, error) {
	return 0, ErrUnknownClaim
}

// ledgerClaimer is the production claimer: the duplicate guard and press claim
// over the submissions ledger and the tracker.
type ledgerClaimer struct{ l *ledger.Ledger }

func (c ledgerClaimer) Claim(ctx context.Context, req PressRequest) (PressGrant, error) {
	cl, err := c.l.Claim(ctx, ledger.ClaimRequest{URL: req.URL, Host: req.Host, Company: req.Company, Role: req.Role})
	var blocked *ledger.BlockedError
	switch {
	case errors.As(err, &blocked):
		return PressGrant{}, &DuplicateSubmitError{Reason: blocked.Decision.Reason}
	case err != nil:
		return PressGrant{}, ErrClaimUnavailable
	}
	return PressGrant{ID: cl.ID, ExpiresAt: cl.ExpiresAt}, nil
}

func (c ledgerClaimer) Settle(ctx context.Context, claimID string, o ledger.Observation) (int64, error) {
	return c.l.Settle(ctx, claimID, o)
}

// claimer is the one Config.PressClaimer names, else the ledger claimer once
// the domain is ready, else one that never grants.
func (s *Server) claimer() PressClaimer {
	if s.cfg.PressClaimer != nil {
		return s.cfg.PressClaimer
	}
	if d := s.domain.Load(); d != nil {
		return ledgerClaimer{d.ledger}
	}
	return unavailableClaimer{}
}

// postingTarget validates a page address and host and drops the query and
// fragment, which can carry identifiers.
func postingTarget(rawURL, host string) (page, hostName string, e *apikit.Error) {
	if e := apikit.CheckPostingURL(rawURL); e != nil {
		return "", "", e
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User != nil {
		return "", "", apikit.InvalidField("url", "url must be an http or https address without credentials")
	}
	hostName = strings.ToLower(strings.TrimSpace(host))
	if len(hostName) > 253 || !hostPattern.MatchString(hostName) {
		return "", "", apikit.InvalidField("host", "host must be a DNS name such as jobs.example.com")
	}
	if strings.ToLower(u.Hostname()) != hostName {
		return "", "", apikit.InvalidField("url", "url must be on the host")
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return u.String(), hostName, nil
}

func checkLen(field, value string) *apikit.Error {
	if len([]rune(value)) > maxSubmissionField {
		return apikit.InvalidField(field, "%s must be at most %d characters", field, maxSubmissionField)
	}
	return nil
}

// claimPress asks permission to press the final submit control. Only an
// explicit grant lets the caller press; a refusal and an unreachable guard are
// both errors, and the caller must not press on any of them.
func (s *Server) claimPress(c *apikit.Call) (apikit.Reply, error) {
	if e := apikit.CheckSubmissionPrincipal(c.P); e != nil {
		return apikit.Reply{}, e
	}
	var req wire.PressClaimRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	page, host, e := postingTarget(req.URL, req.Host)
	if e != nil {
		return apikit.Reply{}, e
	}
	for _, f := range [][2]string{{"company", req.Company}, {"role", req.Role}} {
		if e := checkLen(f[0], f[1]); e != nil {
			return apikit.Reply{}, e
		}
	}
	grant, err := s.claimer().Claim(c.R.Context(), PressRequest{URL: page, Host: host, Company: strings.TrimSpace(req.Company), Role: strings.TrimSpace(req.Role)})
	var dup *DuplicateSubmitError
	switch {
	case errors.As(err, &dup):
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", dup.Error()), wire.ReasonDuplicateSubmit)
	case err != nil || grant.ID == "":
		e := apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "the duplicate check and press claim are not available, so nothing was pressed"), wire.ReasonClaimUnavailable)
		e.Retryable = false
		return apikit.Reply{}, e
	}
	return apikit.Reply{Data: wire.PressClaimGranted{ClaimID: grant.ID, ExpiresAt: wire.Time{Time: grant.ExpiresAt.UTC()}}}, nil
}

func (s *Server) recordSubmission(c *apikit.Call) (apikit.Reply, error) {
	if e := apikit.CheckSubmissionPrincipal(c.P); e != nil {
		return apikit.Reply{}, e
	}
	var req wire.SubmissionRecord
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	page, host, e := postingTarget(req.URL, req.Host)
	if e != nil {
		return apikit.Reply{}, e
	}
	if n := len(req.EntryID); n < 1 || n > 100 {
		return apikit.Reply{}, apikit.InvalidField("entryId", "entryId must be 1 to 100 characters")
	}
	for _, f := range [][2]string{{"company", req.Company}, {"role", req.Role}, {"title", req.Title}} {
		if e := checkLen(f[0], f[1]); e != nil {
			return apikit.Reply{}, e
		}
	}
	if req.ObservedAt.IsZero() {
		return apikit.Reply{}, apikit.InvalidField("observedAt", "observedAt is required")
	}
	if req.Score != nil && (*req.Score < 0 || *req.Score > 5) {
		return apikit.Reply{}, apikit.InvalidField("score", "score must be between 0 and 5")
	}
	if req.ReportNumber != nil && *req.ReportNumber < 1 {
		return apikit.Reply{}, apikit.InvalidField("reportNumber", "reportNumber must be a positive whole number")
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	obs := ledger.Observation{EntryID: req.EntryID, URL: page, Host: host, Company: req.Company, Role: req.Role, Title: req.Title,
		Score: req.Score, ObservedAt: req.ObservedAt.Time, Confirmed: req.Confirmed}
	if req.ReportNumber != nil {
		n := int64(*req.ReportNumber)
		obs.ReportNumber = &n
	}
	var out wire.SubmissionRecorded
	if req.ClaimID != "" {
		id, err := s.claimer().Settle(c.R.Context(), req.ClaimID, obs)
		if errors.Is(err, ErrUnknownClaim) {
			return apikit.Reply{}, apikit.WithReason(apikit.InvalidField("claimId", "claimId names no live press claim"), wire.ReasonUnknownClaim)
		}
		if err != nil {
			return apikit.Reply{}, apikit.Unavailable("the submission could not be recorded; retry")
		}
		out = wire.SubmissionRecorded{LedgerID: apikit.Safe64(id), Result: wire.SubmissionSettled}
	} else {
		rec, err := d.ledger.RecordObserved(c.R.Context(), obs)
		switch {
		case errors.Is(err, ledger.ErrInvalid):
			return apikit.Reply{}, apikit.InvalidField("url", "%s", strings.TrimPrefix(err.Error(), ledger.ErrInvalid.Error()+": "))
		case err != nil:
			return apikit.Reply{}, apikit.Unavailable("the submission could not be recorded; retry")
		}
		out = wire.SubmissionRecorded{LedgerID: apikit.Safe64(rec.ID), Result: string(rec.Result)}
	}
	out.Tracker, err = s.trackerEffect(c.R.Context(), d.tracker, req, page)
	if err != nil {
		// The ledger row stands; the tracker follows on the next record or by hand.
		s.log.Warn("submission tracker effect failed", "error", err.Error())
		out.Tracker = wire.TrackerUnchanged
	}
	if out.Tracker != wire.TrackerUnchanged {
		s.notify("tracker")
	}
	return apikit.Reply{Data: out}, nil
}

// trackerEffect applies the user's submission to the tracker: an evaluated or
// skipped row becomes applied, a missing row is created applied when the
// company and role are known, and a row already applied or later stays.
func (s *Server) trackerEffect(ctx context.Context, tr *tracker.Tracker, req wire.SubmissionRecord, page string) (string, error) {
	found, err := tr.Lookup(ctx, page)
	if err != nil {
		return "", err
	}
	var entry *tracker.Entry
	created := false
	// The application date is the local day the submission was observed on.
	on := req.ObservedAt.In(time.Local).Format(time.DateOnly)
	switch {
	case found.Found:
		entry = found.Entry
	case strings.TrimSpace(req.Company) != "" && strings.TrimSpace(req.Role) != "":
		e, err := tr.Add(ctx, tracker.AddRow{Company: req.Company, Role: req.Role, URL: page, Status: tracker.Applied, AppliedAt: on, Source: "extension"})
		var dup *tracker.DuplicateError
		switch {
		case errors.As(err, &dup):
			d, err := tr.Get(ctx, dup.ID)
			if err != nil {
				return "", err
			}
			entry = &d.Entry
		case err != nil:
			return "", err
		default:
			entry, created = &e, true
		}
	default:
		return wire.TrackerUnchanged, nil
	}
	if created {
		return wire.TrackerCreated, nil
	}
	if entry.Status != tracker.Evaluated && entry.Status != tracker.Skipped {
		return wire.TrackerUnchanged, nil
	}
	res, err := tr.SetStatus(ctx, tracker.StatusChange{ID: entry.ID, To: tracker.Applied, Actor: tracker.ActorUser, On: on})
	if err != nil || !res.Changed {
		return wire.TrackerUnchanged, err
	}
	return wire.TrackerApplied, nil
}

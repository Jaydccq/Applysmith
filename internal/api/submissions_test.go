package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/ledger"
)

const (
	submissionsPath = "/api/submissions"
	claimsPath      = "/api/submissions/claims"
	postingURL      = "https://jobs.example.com/acme/roles/42"
)

func submission(entry string, confirmed bool) map[string]any {
	return map[string]any{"entryId": entry, "url": postingURL + "?utm_source=mail#apply", "host": "jobs.example.com", "company": "Acme Labs",
		"role": "Platform Engineer", "title": "Platform Engineer at Acme Labs", "observedAt": "2026-01-02T03:04:05.000Z", "confirmed": confirmed}
}

func (h *harness) record(body map[string]any) (wire.SubmissionRecorded, result) {
	h.t.Helper()
	r := h.post(submissionsPath, body)
	var env wire.Success[wire.SubmissionRecorded]
	if r.StatusCode == 200 {
		strictInto(h.t, r.body, &env)
	}
	return env.Data, r
}

func (h *harness) ledgerRows() int {
	h.t.Helper()
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM submissions`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestSubmissionRecordCreatesAConfirmedLedgerRowAndAppliedTrackerRow(t *testing.T) {
	h := newDomainHarness(t)
	got, r := h.record(submission("e1", true))
	if r.StatusCode != 200 || got.Result != "created" || got.LedgerID < 1 || got.Tracker != wire.TrackerCreated {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var key, state, source, url string
	var confirmed int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT identity_key, state, source, confirmed, url FROM submissions`).Scan(&key, &state, &source, &confirmed, &url); err != nil {
		t.Fatal(err)
	}
	if key == "" || state != "submitted" || source != "user-observed" || confirmed != 1 {
		t.Fatalf("%s %s %s %d", key, state, source, confirmed)
	}
	if strings.Contains(url, "utm_source") || strings.Contains(url, "#") {
		t.Errorf("the query or fragment was kept: %s", url)
	}
	row := h.get("/api/applications/lookup?url=" + postingURL).data()["entry"].(map[string]any)
	if row["status"] != "applied" || row["appliedBy"] != "user" || row["company"] != "Acme Labs" {
		t.Fatalf("tracker row %v", row)
	}
}

// The lookup carries the submission the applied banner quotes, so an
// unconfirmed click is never shown as a confirmed application.
func TestApplicationLookupCarriesTheSubmissionTheBannerQuotes(t *testing.T) {
	h := newDomainHarness(t)
	if _, ok := h.get("/api/applications/lookup?url=" + postingURL).data()["submission"]; ok {
		t.Fatal("a posting with no submission carries one")
	}
	h.record(submission("e1", false))
	sub := h.get("/api/applications/lookup?url=" + postingURL).data()["submission"].(map[string]any)
	if sub["confirmed"] != false || sub["source"] != "user-observed" || sub["at"] == "" {
		t.Fatalf("unconfirmed: %v", sub)
	}
	h.record(submission("e1", true))
	sub = h.get("/api/applications/lookup?url=" + postingURL).data()["submission"].(map[string]any)
	if sub["confirmed"] != true {
		t.Fatalf("confirmed: %v", sub)
	}
}

func TestSubmissionRecordDedupesAReplayAndNeverDowngrades(t *testing.T) {
	h := newDomainHarness(t)
	first, _ := h.record(submission("e1", false))
	again, _ := h.record(submission("e1", false))
	if again.Result != "duplicate_attached" || again.LedgerID != first.LedgerID || h.ledgerRows() != 1 {
		t.Fatalf("replay: %+v rows %d", again, h.ledgerRows())
	}
	up, _ := h.record(submission("e1", true))
	if up.Result != "upgraded" || up.LedgerID != first.LedgerID {
		t.Fatalf("upgrade: %+v", up)
	}
	down, _ := h.record(submission("e1", false))
	if down.Result != "duplicate_attached" {
		t.Fatalf("downgrade: %+v", down)
	}
	var confirmed int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT confirmed FROM submissions`).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("confirmed %d %v", confirmed, err)
	}
}

func TestSubmissionRecordOfAPostingWithAConfirmedRowAttachesInsteadOfAddingOne(t *testing.T) {
	h := newDomainHarness(t)
	first, _ := h.record(submission("e1", true))
	second, r := h.record(submission("e2", true))
	if r.StatusCode != 200 || second.Result != "duplicate_attached" || second.LedgerID != first.LedgerID || h.ledgerRows() != 1 {
		t.Fatalf("%d %s rows %d", r.StatusCode, r.body, h.ledgerRows())
	}
	// A different posting is its own row.
	other := submission("e3", true)
	other["url"] = "https://jobs.example.com/acme/roles/43"
	if got, _ := h.record(other); got.Result != "created" || h.ledgerRows() != 2 {
		t.Fatalf("%+v rows %d", got, h.ledgerRows())
	}
}

func TestSubmissionRecordIsNotRefusedByTheDuplicateGuard(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.PressClaimer = &fakeClaimer{claimErr: &DuplicateSubmitError{Reason: "already submitted"}}
	})
	if got, r := h.record(submission("e1", true)); r.StatusCode != 200 || got.Result != "created" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSubmissionRecordPromotesAnEvaluatedRowAndLeavesLaterOnesAlone(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Platform Engineer", postingURL)
	got, _ := h.record(submission("e1", true))
	if got.Tracker != wire.TrackerApplied {
		t.Fatalf("%+v", got)
	}
	r := h.get("/api/candidacies/" + idOf(row)).data()["entry"].(map[string]any)
	// Applied on the local day the submission was observed on.
	day := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).In(time.Local).Format(time.DateOnly)
	if r["status"] != "applied" || r["appliedBy"] != "user" || r["appliedAt"] != day {
		t.Fatalf("%v", r)
	}
	h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "interview"})
	again := submission("e2", true)
	again["url"] = postingURL + "?ref=x"
	if got, _ := h.record(again); got.Tracker != wire.TrackerUnchanged {
		t.Fatalf("a later status moved: %+v", got)
	}
	if r := h.get("/api/candidacies/" + idOf(row)).data()["entry"].(map[string]any); r["status"] != "interview" {
		t.Fatalf("%v", r)
	}
}

func TestSubmissionRecordWithoutCompanyAndRoleCreatesNoTrackerRow(t *testing.T) {
	h := newDomainHarness(t)
	body := submission("e1", true)
	delete(body, "company")
	delete(body, "role")
	got, r := h.record(body)
	if r.StatusCode != 200 || got.Result != "created" || got.Tracker != wire.TrackerUnchanged {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if n := h.get("/api/candidacies").data()["total"].(float64); n != 0 {
		t.Fatalf("tracker rows %v", n)
	}
}

func TestSubmissionRecordRefusals(t *testing.T) {
	h := newDomainHarness(t)
	mutate := func(k string, v any) map[string]any {
		b := submission("e1", true)
		if v == nil {
			delete(b, k)
		} else {
			b[k] = v
		}
		return b
	}
	for name, body := range map[string]map[string]any{
		"relative url":     mutate("url", "/apply/1"),
		"ftp url":          mutate("url", "ftp://jobs.example.com/a"),
		"credentials":      mutate("url", "https://user:pw@jobs.example.com/a"),
		"url too long":     mutate("url", "https://jobs.example.com/"+strings.Repeat("a", 2100)),
		"other host":       mutate("host", "careers.example.org"),
		"bad host":         mutate("host", "localhost"),
		"no entry id":      mutate("entryId", ""),
		"long entry id":    mutate("entryId", strings.Repeat("e", 101)),
		"no observed time": mutate("observedAt", nil),
		"local time":       mutate("observedAt", "2026-01-02T03:04:05"),
		"bad score":        mutate("score", 9),
		"bad report":       mutate("reportNumber", 0),
		"long title":       mutate("title", strings.Repeat("t", 501)),
		"form value":       mutate("formValues", map[string]any{"name": "x"}),
	} {
		if r := h.post(submissionsPath, body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if h.ledgerRows() != 0 {
		t.Errorf("a refused record wrote a row")
	}
	if r := h.do(call{method: "POST", path: submissionsPath, useToken: true, contentType: "text/plain", body: []byte("x")}); r.StatusCode != 415 {
		t.Errorf("content type: %d", r.StatusCode)
	}
	if resp, b := h.postExpecting(submissionsPath, "application/json", []byte(strings.Repeat(" ", 300_000))); resp.StatusCode != 413 {
		t.Errorf("size: %d %s", resp.StatusCode, b)
	}
}

func TestSubmissionRecordKeepsOneRowUnderConcurrentClicks(t *testing.T) {
	h := newDomainHarness(t)
	var wg sync.WaitGroup
	replies := make([]result, 6)
	for i := range replies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = h.post(submissionsPath, submission("same", true))
		}()
	}
	wg.Wait()
	// The replies are decoded here: a failed decode inside a goroutine would
	// call t.Fatal off the test's goroutine.
	results := make([]string, len(replies))
	created := 0
	for i, r := range replies {
		if r.StatusCode == 200 {
			var env wire.Success[wire.SubmissionRecorded]
			strictInto(t, r.body, &env)
			results[i] = env.Data.Result
		}
		if results[i] == "created" {
			created++
		}
	}
	if created != 1 || h.ledgerRows() != 1 {
		t.Fatalf("results %v rows %d", results, h.ledgerRows())
	}
}

func TestSubmissionRoutesTurnAwayOtherPrincipals(t *testing.T) {
	h := newDomainHarness(t)
	for name, fn := range map[string]apikit.Handler{"record": h.srv.recordSubmission, "claim": h.srv.claimPress, "signin": h.srv.resolveSignin, "otp": h.srv.gmail.LatestOTP} {
		_, err := fn(&apikit.Call{P: apikit.Principal{Kind: apikit.PrincipalRemoteDevice}, R: httptestRequest(t)})
		var ae *apikit.Error
		if !errors.As(err, &ae) || ae.Code != wire.CodeForbidden {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// fakeClaimer stands in for the apply-safety gates.
type fakeClaimer struct {
	mu       sync.Mutex
	claims   []PressRequest
	settled  []string
	grantID  string
	claimErr error
}

func (f *fakeClaimer) Claim(_ context.Context, req PressRequest) (PressGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, req)
	if f.claimErr != nil {
		return PressGrant{}, f.claimErr
	}
	return PressGrant{ID: f.grantID, ExpiresAt: time.Date(2026, 1, 2, 3, 19, 5, 0, time.UTC)}, nil
}

func (f *fakeClaimer) Settle(_ context.Context, id string, _ ledger.Observation) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.grantID {
		return 0, ErrUnknownClaim
	}
	f.settled = append(f.settled, id)
	return 77, nil
}

func claimBody() wire.PressClaimRequest {
	return wire.PressClaimRequest{URL: postingURL + "?token=abc#step", Host: "jobs.example.com", Company: "Acme Labs", Role: "Platform Engineer"}
}

func TestPressClaimGrantsOnlyWhenTheClaimerGrants(t *testing.T) {
	fc := &fakeClaimer{grantID: "claim-1"}
	h := newDomainHarness(t, func(c *Config) { c.PressClaimer = fc })
	r := h.post(claimsPath, claimBody())
	var env wire.Success[wire.PressClaimGranted]
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.ClaimID != "claim-1" || env.Data.ExpiresAt.IsZero() {
		t.Fatalf("%+v", env.Data)
	}
	if len(fc.claims) != 1 || fc.claims[0].URL != postingURL || fc.claims[0].Host != "jobs.example.com" || fc.claims[0].Company != "Acme Labs" {
		t.Fatalf("the claimer saw %+v", fc.claims)
	}
}

func TestPressClaimFailsClosed(t *testing.T) {
	for name, c := range map[string]struct {
		claimer PressClaimer
		status  int
		code    string
		reason  string
	}{
		"guard unreachable":   {&fakeClaimer{claimErr: ErrClaimUnavailable}, 503, "unavailable", wire.ReasonClaimUnavailable},
		"unexpected failure":  {&fakeClaimer{claimErr: errors.New("ledger locked")}, 503, "unavailable", wire.ReasonClaimUnavailable},
		"duplicate":           {&fakeClaimer{claimErr: &DuplicateSubmitError{Reason: "already submitted"}}, 409, "conflict", wire.ReasonDuplicateSubmit},
		"grant without an id": {&fakeClaimer{}, 503, "unavailable", wire.ReasonClaimUnavailable},
	} {
		h := newDomainHarness(t, func(cfg *Config) { cfg.PressClaimer = c.claimer })
		r := h.post(claimsPath, claimBody())
		if r.StatusCode != c.status || r.errCode() != c.code || r.errReason() != c.reason || strings.Contains(string(r.body), "claimId") {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestPressClaimNamesTheReasonForADuplicate(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.PressClaimer = &fakeClaimer{claimErr: &DuplicateSubmitError{Reason: "already submitted"}}
	})
	r := h.post(claimsPath, claimBody())
	if !strings.Contains(string(r.body), "already submitted") {
		t.Fatalf("the person is not told why: %s", r.body)
	}
}

func TestPressClaimRefusals(t *testing.T) {
	fc := &fakeClaimer{grantID: "claim-1"}
	h := newDomainHarness(t, func(c *Config) { c.PressClaimer = fc })
	for name, b := range map[string]wire.PressClaimRequest{
		"relative url": {URL: "/x", Host: "jobs.example.com"},
		"other host":   {URL: postingURL, Host: "careers.example.org"},
		"no host":      {URL: postingURL},
		"long company": {URL: postingURL, Host: "jobs.example.com", Company: strings.Repeat("c", 501)},
	} {
		if r := h.post(claimsPath, b); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if len(fc.claims) != 0 {
		t.Errorf("the claimer was asked about an invalid request")
	}
}

func TestSubmissionRecordWithAClaimSettlesIt(t *testing.T) {
	fc := &fakeClaimer{grantID: "claim-1"}
	h := newDomainHarness(t, func(c *Config) { c.PressClaimer = fc })
	body := submission("e1", false)
	body["claimId"] = "claim-1"
	got, r := h.record(body)
	if r.StatusCode != 200 || got.Result != wire.SubmissionSettled || got.LedgerID != 77 || got.Tracker != wire.TrackerCreated || len(fc.settled) != 1 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	body["claimId"] = "claim-other"
	if r := h.post(submissionsPath, body); r.StatusCode != 422 || r.errReason() != wire.ReasonUnknownClaim {
		t.Errorf("unknown claim: %d %s", r.StatusCode, r.body)
	}
}

func TestSubmissionRecordWithAClaimIdIsRefusedWhenNoClaimerExists(t *testing.T) {
	h := newDomainHarness(t)
	body := submission("e1", false)
	body["claimId"] = "claim-1"
	if r := h.post(submissionsPath, body); r.StatusCode != 422 || r.errReason() != wire.ReasonUnknownClaim || h.ledgerRows() != 0 {
		t.Fatalf("%d %s rows %d", r.StatusCode, r.body, h.ledgerRows())
	}
}

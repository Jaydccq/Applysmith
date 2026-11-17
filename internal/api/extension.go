package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/extension"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

const (
	// fillRunIdle is how long an extension fill's run may go without a match,
	// system-write or learning call before the server ends it. The page step is
	// bounded to 300 s, so a live fill never gets near it.
	fillRunIdle = 15 * time.Minute
	// maxOpenFills bounds the runs a client can leave open at once.
	maxOpenFills   = 16
	fillSweepEvery = time.Minute
	maxFillURL     = 2048
)

// hostPattern is a lowercase DNS name with at least one dot.
var hostPattern = lazyregexp.New(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// autofillApplicant serves the snapshot the engine starts a fill with. It
// holds identity and history only; every other answer goes through the matcher.
func (s *Server) autofillApplicant(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	a, err := d.bank.Applicant(s.now())
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	return apikit.Reply{Data: ApplicantSnapshot(a)}, nil
}

// ApplicantSnapshot is the wire form of the applicant the engine starts with.
// Its education is most recent first, the matcher's row order, so the
// engine's own row plan puts each school on the row the matcher answers for it.
func ApplicantSnapshot(a answerbank.Applicant) wire.ApplicantSnapshot {
	out := wire.ApplicantSnapshot{
		Candidate: wire.ApplicantCandidate{FullName: a.FullName, FirstName: a.FirstName, MiddleName: a.MiddleName, LastName: a.LastName,
			PreferredName: a.PreferredName, Email: a.Email, Phone: a.Phone, PhoneCountryCode: a.PhoneCountryCode, PhoneNational: a.PhoneNational,
			Address: wire.ApplicantAddress(a.Address)},
		Links:   wire.ApplicantLinks{LinkedIn: a.LinkedIn, GitHub: a.GitHub, Portfolio: a.Portfolio},
		History: wire.ApplicantHistory{Employment: make([]wire.ApplicantJob, 0, len(a.Employment)), Education: make([]wire.ApplicantStudy, 0, len(a.Education))},
		AskMe:   apikit.NonNil(a.AskMe),
	}
	for _, j := range a.Employment {
		out.History.Employment = append(out.History.Employment, wire.ApplicantJob(j))
	}
	for _, e := range match.RecentFirst(a.Education, func(e answerbank.ApplicantStudy) string { return e.EndDate }) {
		out.History.Education = append(out.History.Education, wire.ApplicantStudy(e))
	}
	return out
}

// boardPostingWait bounds the one board request of a posting read.
const boardPostingWait = 10 * time.Second

// readBoardPosting reads a posting from its board's public API, the reader the
// evaluation uses.
func (s *Server) readBoardPosting(ctx context.Context, postingURL string) (evaluate.PageRead, error) {
	r := evaluate.ATSReader{Client: s.cfg.Domain.ScanHTTP, Hosts: s.cfg.Domain.ScanEndpoints}
	return r.Read(ctx, postingURL)
}

// autofillPosting serves the posting of a page that holds only the application
// form (a Lever /apply or Ashby /application page), read from its public
// board: Lever and Ashby addresses only, one request, at most 10 s. A board
// that cannot be read is unavailable, and the extension keeps the page's own
// reading. A Greenhouse page shows its description, and its API answers HTML.
func (s *Server) autofillPosting(c *apikit.Call) (apikit.Reply, error) {
	raw := c.R.URL.Query().Get("url")
	key, ok := identity.ATSKey(raw)
	ats, _, _ := strings.Cut(key, ":")
	if len(raw) > maxFillURL || !ok || (ats != identity.Lever && ats != identity.Ashby) {
		return apikit.Reply{}, apikit.InvalidField("url", "url must be a Lever or Ashby posting address")
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), boardPostingWait)
	defer cancel()
	p, err := s.readBoardPosting(ctx, raw)
	if err != nil {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "the posting's board could not be read"), "board-unreadable")
	}
	return apikit.Reply{Data: wire.PostingRead{Title: p.Title, Location: p.Location, Text: strings.TrimSpace(p.Text)}}, nil
}

// extensionBuild names the extension this application carries, so the popup
// can tell the user when the loaded one is another build.
func (s *Server) extensionBuild(c *apikit.Call) (apikit.Reply, error) {
	read := func() (wire.ExtensionBuild, error) {
		b, err := extension.Build()
		return wire.ExtensionBuild{Stamp: b.Stamp, Version: b.Version}, err
	}
	if s.cfg.ExtensionBuild != nil {
		read = s.cfg.ExtensionBuild
	}
	b, err := read()
	if errors.Is(err, extension.ErrNotBuilt) {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "this build carries no extension; run make generate"), "extension-not-built")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: b}, nil
}

// extensionHeader names the install a request comes from (extension.md).
const extensionHeader = "X-Applysmith-Extension"

var clientField = lazyregexp.New(`^[A-Za-z0-9._+-]{1,64}$`)

// extensionClients is presence, not pairing: every paired extension holds the
// same token. Memory is enough, as a paired extension checks in every 30 s.
type extensionClients struct {
	mu   sync.Mutex
	byID map[string]wire.ExtensionClient
}

func lastSeenFirst(a, b wire.ExtensionClient) int { return b.LastSeen.Compare(a.LastSeen.Time) }

// seen records the install a request named; a malformed header is ignored.
func (x *extensionClients) seen(header string, at time.Time) {
	var c wire.ExtensionClient
	for part := range strings.SplitSeq(header, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if !clientField.MatchString(v) {
			continue
		}
		switch k {
		case "id":
			c.ID = v
		case "version":
			c.Version = v
		case "build":
			c.Build = v
		case "managed":
			c.Managed = v == "1"
		}
	}
	if len(c.ID) < 8 {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.byID == nil {
		x.byID = map[string]wire.ExtensionClient{}
	}
	if _, ok := x.byID[c.ID]; !ok && len(x.byID) >= 8 {
		all := x.list()
		delete(x.byID, all[len(all)-1].ID)
	}
	c.LastSeen = wire.Time{Time: at}
	x.byID[c.ID] = c
}

// list is most recently seen first; the caller holds mu.
func (x *extensionClients) list() []wire.ExtensionClient {
	return append([]wire.ExtensionClient{}, slices.SortedFunc(maps.Values(x.byID), lastSeenFirst)...)
}

func (x *extensionClients) forget() {
	x.mu.Lock()
	x.byID = nil
	x.mu.Unlock()
}

func (s *Server) listExtensionClients(c *apikit.Call) (apikit.Reply, error) {
	s.exts.mu.Lock()
	defer s.exts.mu.Unlock()
	return apikit.Reply{Data: wire.ExtensionClients{Since: wire.Time{Time: s.startedAt}, Clients: s.exts.list()}}, nil
}

// unpairExtensions replaces the token every paired extension shares; the
// command line and the MCP server read the new one from the vault.
func (s *Server) unpairExtensions(c *apikit.Call) (apikit.Reply, error) {
	_, err := s.RotateToken()
	switch {
	case errors.Is(err, ErrEnvManaged):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the API token is supplied by APPLYSMITH_SECRET_API_TOKEN; change it there")
	case err != nil:
		return apikit.Reply{}, err
	}
	return s.listExtensionClients(c)
}

// fillTarget validates the host and page of a fill and drops the query and
// fragment of the page, which can carry identifiers.
func fillTarget(req wire.FillRunRequest) (host, page string, e *apikit.Error) {
	host = strings.ToLower(strings.TrimSpace(req.Host))
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return "", "", apikit.InvalidField("host", "host must be a DNS name such as jobs.example.com")
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	switch {
	case len(req.URL) > maxFillURL:
		return "", "", apikit.InvalidField("url", "url must be at most %d characters", maxFillURL)
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil:
		return "", "", apikit.InvalidField("url", "url must be an http or https address without credentials")
	case strings.ToLower(u.Hostname()) != host:
		return "", "", apikit.InvalidField("url", "url must be on the host")
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return host, u.String(), nil
}

// createFillRun issues the run that an extension fill's match, system-write and
// learning calls name. The run does no work and holds no authority to submit;
// it exists so those calls can be attributed and so the board can show the
// fill.
func (s *Server) createFillRun(c *apikit.Call) (apikit.Reply, error) {
	if c.P.Kind != apikit.PrincipalDashboard && c.P.Kind != apikit.PrincipalLocalToken {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "only a paired client or the dashboard may start a fill")
	}
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.FillRunRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	host, page, e := fillTarget(req)
	if e != nil {
		return apikit.Reply{}, e
	}
	body, err := json.Marshal(wire.FillRunRequest{Host: host, URL: page})
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	ref, err := st.CreateRun(c.R.Context(), store.RunRequest{
		Principal: c.P.ID, Operation: "autofill.fill", Key: key, Kind: wire.RunKindExtensionFill, Input: body,
		Admit: func(ctx context.Context) error {
			if n, err := st.CountOpenRuns(ctx, wire.RunKindExtensionFill); err != nil || n >= maxOpenFills {
				if err != nil {
					return err
				}
				return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%d fills are already open; finish one or wait for an idle one to end", n), "too-many-fills")
			}
			return nil
		},
		Created: func(tx *sql.Tx, runID string) error {
			_, err := tx.Exec(`UPDATE runs SET status = ?, stage = 'fill' WHERE id = ?`, wire.RunRunning, runID)
			return err
		},
	})
	if err != nil {
		return apikit.Reply{}, err
	}
	if !ref.Replayed {
		s.notify("runs")
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
}

// endFillRun ends an extension fill as succeeded or cancelled. Ending a run
// that already ended answers with its stored state.
func (s *Server) endFillRun(c *apikit.Call) (apikit.Reply, error) {
	var req wire.FillRunEnd
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.Status != wire.FillSucceeded && req.Status != wire.FillCancelled {
		return apikit.Reply{}, apikit.InvalidField("status", "status must be succeeded or cancelled")
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	run, err := st.GetRun(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	if run.Kind != wire.RunKindExtensionFill {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a %s run is not an extension fill", run.Kind)
	}
	if !terminal(run.Status) {
		if err := s.finishFill(c.R.Context(), st, id, req.Status, ""); err != nil {
			return apikit.Reply{}, err
		}
		if run, err = st.GetRun(c.R.Context(), id); err != nil {
			return apikit.Reply{}, err
		}
	}
	return apikit.Reply{Data: toRunView(run)}, nil
}

// finishFill ends the run and tells the run stream and the board.
func (s *Server) finishFill(ctx context.Context, st *store.Store, id, status, reason string) error {
	if err := st.SetRun(ctx, id, status, "", reason); err != nil {
		return err
	}
	s.runs.publish(id, "final", wire.RunFinal{RunID: id, Status: status, Reason: reason})
	s.runs.retire(id)
	s.notify("runs")
	return nil
}

// touchFill records that a call named the run, which keeps an idle timeout from
// ending a fill that is still working. A failure only shortens the fill's
// life, so it is logged and not returned.
func (s *Server) touchFill(ctx context.Context, runIDs ...string) {
	st := s.store.Load()
	if st == nil {
		return
	}
	seen := map[string]bool{}
	for _, id := range runIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if err := st.TouchRun(ctx, id, wire.RunKindExtensionFill); err != nil && s.log != nil {
			s.log.Warn("fill run not touched", "error", err.Error())
		}
	}
}

// sweepFills ends every extension fill that has been idle for fillRunIdle: a
// worker that died or a tab that closed leaves its run behind.
func (s *Server) sweepFills(ctx context.Context) {
	st := s.store.Load()
	if st == nil {
		return
	}
	ids, err := st.IdleRuns(ctx, wire.RunKindExtensionFill, fillRunIdle)
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := s.finishFill(ctx, st, id, wire.RunCancelled, "idle-timeout"); err != nil && s.log != nil {
			s.log.Warn("idle fill not ended", "error", err.Error())
		}
	}
}

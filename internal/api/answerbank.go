package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

func (s *Server) answerBank() (*answerbank.Service, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.bank, nil
}

// answerBankError maps an answer-bank failure onto the API errors: a bank file
// that does not load is config_invalid, a refused write is validation_failed.
func answerBankError(err error) error {
	var load *answerbank.LoadError
	var invalid *answerbank.ValidationError
	switch {
	case errors.As(err, &load):
		return apikit.NewErr(wire.CodeConfigInvalid, "answer-bank.yml is invalid: %s", load.Error())
	case errors.As(err, &invalid):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", invalid.Error())
	case workspace.IsCode(err, workspace.CodeRevisionConflict):
		return apikit.NewErr(wire.CodeRevisionConflict, "answer-bank.yml changed on disk; reload it and apply the change again")
	case workspace.IsCode(err, workspace.CodePayloadTooLarge):
		return apikit.NewErr(wire.CodePayloadTooLarge, "the bank is larger than %d MiB", answerbank.MaxBankBytes>>20)
	}
	return err
}

// learnError maps a learning failure onto the API errors. A refused decision is
// a conflict that names its reason.
func learnError(err error) error {
	var refused *learn.ConflictError
	switch {
	case errors.As(err, &refused):
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", refused.Message), refused.Reason)
	case errors.Is(err, learn.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "%s", err.Error())
	case errors.Is(err, learn.ErrBatchSize):
		return apikit.NewErr(wire.CodePayloadTooLarge, "an upload holds 1 to %d events; split it", learn.MaxBatch)
	}
	return answerBankError(err)
}

// viaJSON copies a value between the match types and their wire twins. The
// twins carry the same JSON keys, and the strict decode makes a key one side
// lacks an error instead of a silent loss; a test keeps the two in step.
func viaJSON(from, to any) error {
	raw, err := json.Marshal(from)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(to)
}

func toWireBank(b match.Bank) (wire.AnswerBank, error) {
	out := wire.AnswerBank{Version: b.Version, Employers: []wire.AnswerBankEmployer{}, Entries: []wire.AnswerBankEntry{},
		Overrides: map[string][]wire.AnswerBankOverride{}}
	if err := viaJSON(b.Employers, &out.Employers); err != nil {
		return out, err
	}
	if err := viaJSON(b.Entries, &out.Entries); err != nil {
		return out, err
	}
	for key, list := range b.Overrides {
		var ws []wire.AnswerBankOverride
		if err := viaJSON(list, &ws); err != nil {
			return out, err
		}
		out.Overrides[b.HostName(key)] = ws
	}
	if out.Employers == nil {
		out.Employers = []wire.AnswerBankEmployer{}
	}
	if out.Entries == nil {
		out.Entries = []wire.AnswerBankEntry{}
	}
	return out, nil
}

func fromWireBank(w wire.AnswerBank) (match.Bank, error) {
	out := match.Bank{Version: w.Version, Overrides: map[string][]match.Override{}}
	if err := viaJSON(w.Employers, &out.Employers); err != nil {
		return out, err
	}
	if err := viaJSON(w.Entries, &out.Entries); err != nil {
		return out, err
	}
	for host, list := range w.Overrides {
		var os []match.Override
		if err := viaJSON(list, &os); err != nil {
			return out, err
		}
		host = strings.TrimSpace(host)
		key := strings.ToLower(host)
		if out.HostSpelling == nil {
			out.HostSpelling = map[string]string{}
		}
		if _, dup := out.HostSpelling[key]; dup {
			return out, apikit.NewErr(wire.CodeValidationFailed, "two override hosts differ only in case")
		}
		out.HostSpelling[key] = host
		out.Overrides[key] = os
	}
	return out, nil
}

func (s *Server) getAnswerBank(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	l, err := svc.File()
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	bank, err := toWireBank(l.Bank)
	if err != nil {
		return apikit.Reply{}, err
	}
	def, err := answerbank.DefaultBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	ids := make([]string, len(def.Bank.Entries))
	for i, e := range def.Bank.Entries {
		ids[i] = e.ID
	}
	shapes := make(map[string]wire.AnswerBankShape, len(l.Bank.Entries))
	for _, e := range l.Bank.Entries {
		sh := answerbank.ShapeOf(e)
		w := wire.AnswerBankShape{Type: sh.Type, Choices: []wire.AnswerBankChoice{}}
		for _, c := range sh.Choices {
			w.Choices = append(w.Choices, wire.AnswerBankChoice{Label: c.Label, Matches: c.Matches})
		}
		shapes[e.ID] = w
	}
	return apikit.Reply{Data: wire.AnswerBankFile{Exists: l.Exists, Revision: l.Revision, Warnings: apikit.NonNil(l.Warnings), Bank: bank, DefaultIDs: ids, Shapes: shapes}}, nil
}

// defaultsMeta is the workspace_meta row holding the digest of the shipped
// defaults the waiting runs were last told about (moveForDefaults).
const defaultsMeta = "answerbank.defaults-told"

// moveForDefaults tells each waiting run whose every question the bank now
// answers, or the fill answers from the user's information (F-10), that it can
// be retried, once for each set of shipped defaults the server starts with
// (flow round 11, D-4): a load applies the defaults by itself, so no save moves
// the runs a newer default answers, and the server's first start with them
// does, as it becomes ready. The runs are those a saved
// answer would move (answeredRuns), but none goes on: at start every tab reads
// as kept until the browser is read, and nobody is watching. A start that
// cannot read the runs leaves them to the next.
func (s *Server) moveForDefaults(ctx context.Context, st *store.Store) {
	sum := fmt.Sprintf("%x", sha256.Sum256(answerbank.DefaultBytes()))
	var told string
	err := st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, defaultsMeta).Scan(&told)
	if err != nil && !errors.Is(err, sql.ErrNoRows) || told == sum {
		return
	}
	d, err := s.dom()
	if err != nil {
		return
	}
	route, err := s.apply.Router()
	if err != nil {
		return
	}
	waiting, err := s.apply.WaitingRuns(ctx, "", route)
	if err != nil {
		s.log.Warn("the runs waiting for answers could not be read", "error", err.Error())
		return
	}
	before := map[string]int{}
	for _, w := range waiting {
		before[w.ID()] = w.Asked()
	}
	runs, err := s.apply.AnsweredRuns(ctx, before, "")
	if err != nil {
		return
	}
	detail := applyapi.AnsweredDetail
	for _, w := range runs {
		if _, err := d.notify.board.Update(ctx, w.ID(), notify.Patch{Detail: &detail}); err != nil {
			s.log.Warn("an answered run's entry could not say so", "run", w.ID(), "error", err.Error())
		}
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, defaultsMeta, sum)
		return err
	})
	if err != nil {
		s.log.Warn("the shipped defaults the runs were told about could not be recorded", "error", err.Error())
	}
}

// matcherFacts reads the profile values the fill passes the matcher, for the
// views that say what a run's question gets now; an unreadable profile gives
// none.
func matcherFacts(bank *answerbank.Service, tr *tracker.Tracker) func() match.Facts {
	return func() match.Facts {
		p, ok := bank.Profile()
		if !ok {
			return match.Facts{}
		}
		f := bind.FactsFromProfile(p)
		f.Relationships = append(f.Relationships, bind.HistoryFacts(context.Background(), tr)...)
		return f
	}
}

func (s *Server) putAnswerBank(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AnswerBankSave
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.ExpectedRevision == "" {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "expectedRevision is required; read the bank first and quote its revision")
	}
	bank, err := fromWireBank(req.Bank)
	if err != nil {
		var apiErr *apikit.Error
		if errors.As(err, &apiErr) {
			return apikit.Reply{}, apiErr
		}
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "the bank could not be read")
	}
	// A changed answer fits its entry's shape; the answers a form taught
	// earlier stay as they are until they are changed.
	cur, err := svc.File()
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	if err := answerbank.CheckAnswers(cur.Bank, bank); err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	before := s.apply.OpenQuestions(c.R.Context())
	saved, err := svc.Save(bank, req.ExpectedRevision)
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	if saved.Changed {
		s.apply.MoveAnswered(c.R.Context(), before, "", applyapi.DoneFromDashboard)
	}
	return apikit.Reply{Data: wire.AnswerBankSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}

func toAnswerBankGaps(in []match.Gap) []wire.AnswerBankGap {
	out := make([]wire.AnswerBankGap, 0, len(in))
	for _, g := range in {
		out = append(out, wire.AnswerBankGap{ID: g.ID, Category: g.Category, Kind: string(g.Kind), Blocking: g.Blocking})
	}
	return out
}

func (s *Server) answerBankUnanswered(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	l, err := svc.Bank()
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	report := match.NeedsAnswer(l.Bank)
	derived := []wire.AnswerBankDerived{}
	for _, e := range l.Bank.Entries {
		if e.DerivedFrom != "" {
			derived = append(derived, wire.AnswerBankDerived{ID: e.ID, Step: e.DerivedFrom, Answer: derivedAnswer(e)})
		}
	}
	route, err := s.apply.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	runs, err := s.apply.WaitingRuns(c.R.Context(), "", route)
	if err != nil {
		return apikit.Reply{}, err
	}
	asked := applyapi.AskedQuestions(route, runs)
	return apikit.Reply{Data: wire.AnswerBankUnanswered{
		Unanswered:  toAnswerBankGaps(report.Unanswered),
		Blocking:    toAnswerBankGaps(report.Blocking()),
		NeedsReview: toAnswerBankGaps(report.NeedsReview),
		AskMe:       apikit.NonNil(report.AskMe),
		Derived:     derived,
		Asked:       asked,
	}}, nil
}

// derivedAnswer is a derived entry's body as one line of text.
func derivedAnswer(e match.Entry) string {
	switch e.Kind {
	case match.KindPickOption:
		// The first preferred label is the answer; the rest are other labels
		// of the same choice, such as "California (CA)".
		if len(e.Preferred) > 0 {
			return e.Preferred[0]
		}
		return e.FallbackText
	case match.KindCompensationRange:
		return strings.TrimSpace(fmt.Sprintf("%s %s (minimum %s)", e.DefaultAnswer, e.Currency, e.Minimum))
	}
	return e.Answer
}

func toAnswerBankSettings(v answerbank.Settings) wire.AnswerBankSettings {
	return wire.AnswerBankSettings{LLMFallbackEnabled: v.LLMFallbackEnabled, GroundingMode: v.GroundingMode,
		AutoAcceptLegalDeclarations: v.AutoAcceptLegalDeclarations, WatchScans: v.WatchScans, AllowGoogleSignIn: v.AllowGoogleSignIn, KnowledgeSemanticRetrieval: v.KnowledgeSemanticRetrieval}
}

func (s *Server) answerBankSettings(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: toAnswerBankSettings(svc.Settings(c.R.Context()))}, nil
}

func (s *Server) patchAnswerBankSettings(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AnswerBankSettingsPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	before := svc.Settings(c.R.Context())
	got, err := svc.PatchSettings(c.R.Context(), answerbank.SettingsPatch{LLMFallbackEnabled: req.LLMFallbackEnabled,
		GroundingMode: req.GroundingMode, AutoAcceptLegalDeclarations: req.AutoAcceptLegalDeclarations, WatchScans: req.WatchScans, AllowGoogleSignIn: req.AllowGoogleSignIn, KnowledgeSemanticRetrieval: req.KnowledgeSemanticRetrieval})
	if err != nil {
		return apikit.Reply{}, answerBankError(err)
	}
	if got.AutoAcceptLegalDeclarations != before.AutoAcceptLegalDeclarations {
		if err := s.auditLegalSetting(c.R.Context(), c.P, got.AutoAcceptLegalDeclarations); err != nil {
			s.log.Error("the legal declarations setting change could not be audited", "error", err.Error())
		}
	}
	return apikit.Reply{Data: toAnswerBankSettings(got)}, nil
}

// auditLegalSetting appends the change of the legal-declarations setting to
// the gate audit with its time.
func (s *Server) auditLegalSetting(ctx context.Context, p apikit.Principal, on bool) error {
	return s.recordGate(ctx, p, gates.LegalSettingChange(on))
}

func (s *Server) transcript(*apikit.Call) (apikit.Reply, error) {
	name, err := s.cfg.Workspace.Transcript()
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Data: wire.AnswerBankTranscript{FileName: name}}, nil
}

// uploadTranscript stores the uploaded PDF as transcript/<name>.pdf in place
// of the transcript stored before.
func (s *Server) uploadTranscript(c *apikit.Call) (apikit.Reply, error) {
	data, err := io.ReadAll(c.R.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "a transcript PDF is limited to 10 MiB")
		}
		return apikit.Reply{}, err
	}
	name, err := s.cfg.Workspace.StoreTranscript(apikit.QueryOf(c).Str("name"), data)
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Data: wire.AnswerBankTranscript{FileName: name}}, nil
}

func (s *Server) removeTranscript(*apikit.Call) (apikit.Reply, error) {
	if err := s.cfg.Workspace.RemoveTranscript(); err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Data: wire.AnswerBankTranscript{}}, nil
}

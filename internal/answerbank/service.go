package answerbank

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// MaxBankBytes is the largest bank file a save accepts; request bodies for the
// bank must allow at least this much.
const MaxBankBytes = 8 << 20

// Service owns answer-bank.yml of one workspace and the runtime settings in
// its state store. Every write of the file goes through one mutex and the
// workspace's revisioned atomic writer, so two saves can never interleave.
type Service struct {
	ws *workspace.Workspace
	st *store.Store // nil in read-only callers; settings then need a store

	mu   sync.Mutex // serialises load-modify-write of the file
	last *Loaded    // the last bank that parsed, kept when a reload fails

	profileMu  sync.Mutex
	profileRev string
	profile    workspace.Profile
	profileOK  bool

	now func() time.Time
}

// Loaded is a parsed bank with the revision of the file it came from.
type Loaded struct {
	Result
	// Exists is false when the file is absent.
	Exists   bool
	Revision string
}

// New builds a service. st may be nil when only the bank file is used.
func New(ws *workspace.Workspace, st *store.Store) *Service {
	return &Service{ws: ws, st: st, now: time.Now}
}

// Seed writes the embedded default bank when the workspace has no bank file.
// It never overwrites an existing file, whatever it holds, and reports whether
// it created one. Call it from server start and setup, never from a read.
func (s *Service) Seed() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.ws.Path(workspace.FileAnswerBank)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	return createIfAbsent(path, defaultBankYAML)
}

// createIfAbsent publishes data under path atomically: a reader sees the whole
// file or none, and an existing file is left alone.
func createIfAbsent(path string, data []byte) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// File loads the bank as written with the shipped defaults applied in memory
// (withDefaults), without the profile stamps: the bank every match reads and
// every writer changes, since a save writes only what its writer changed
// (asWritten). The warnings are the file's. A file that fails to load returns
// the bank that last loaded together with the error, so the caller keeps
// working with a known-good bank. A missing file is the empty bank with Exists
// false, no warning, and nothing is created.
func (s *Service) File() (Loaded, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileLocked()
}

func (s *Service) fileLocked() (Loaded, error) {
	c, err := s.ws.Read(workspace.FileAnswerBank)
	if err != nil {
		return s.lastGood(), err
	}
	if c.Revision == workspace.RevisionAbsent {
		l := Loaded{Result: Result{Bank: match.Bank{Version: 1, Overrides: map[string][]match.Override{}}}, Revision: c.Revision}
		s.last = &l
		return l.clone(), nil
	}
	if s.last != nil && s.last.Revision == c.Revision {
		return s.last.clone(), nil
	}
	res, err := Parse(workspace.FileAnswerBank.Name(), c.Data)
	if err == nil {
		res, _, err = withDefaults(res, c.Data)
	}
	if err != nil {
		return s.lastGood(), err
	}
	l := Loaded{Result: res, Exists: true, Revision: c.Revision}
	s.last = &l
	return l.clone(), nil
}

// clone copies the bank and the warnings, so a caller that changes what it
// gets can never change the bank the service keeps.
func (l Loaded) clone() Loaded {
	l.Bank = l.Bank.Clone()
	l.Warnings = slices.Clone(l.Warnings)
	return l
}

func (s *Service) lastGood() Loaded {
	if s.last != nil {
		return s.last.clone()
	}
	return Loaded{Result: Result{Bank: match.Bank{Version: 1, Overrides: map[string][]match.Override{}}}}
}

// Bank loads the bank and stamps it with the preferences of the profile as it
// is now, so profile edits take effect on the next call. Use File for the
// view that is saved back.
func (s *Service) Bank() (Loaded, error) {
	l, err := s.File()
	l.Bank = l.Bank.Stamp(s.preferences())
	return l, err
}

// Unanswered applies the needs-answer rule to the stamped bank.
func (s *Service) Unanswered() (match.NeedsReport, error) {
	l, err := s.Bank()
	return match.NeedsAnswer(l.Bank), err
}

// ValidationError is a refused save: it names every entry that failed.
type ValidationError struct{ Issues []Issue }

// Issue is one problem found by validation.
type Issue struct {
	EntryID string
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		parts = append(parts, i.String())
	}
	return "validation_failed: " + strings.Join(parts, "; ")
}

func (i Issue) String() string {
	if i.EntryID != "" {
		return fmt.Sprintf("entry %q: %s", i.EntryID, i.Message)
	}
	return i.Message
}

// Save validates b, restores the bodies the load stamped, and replaces the
// file when expected matches its revision (empty means the revision read under
// the writer lock). Runtime values are never written.
func (s *Service) Save(b match.Bank, expected string) (workspace.Saved, error) {
	return s.save(b, expected, Validate)
}

// SaveChange is Save for a change made on top of before: only the validation
// issues the change introduces refuse it, so a problem the bank already had
// never holds every later change hostage.
func (s *Service) SaveChange(before, after match.Bank, expected string) (workspace.Saved, error) {
	return s.save(after, expected, func(b match.Bank) error {
		err := Validate(b)
		var now, was *ValidationError
		if !errors.As(err, &now) {
			return err
		}
		known := map[Issue]bool{}
		if errors.As(Validate(before), &was) {
			for _, i := range was.Issues {
				known[i] = true
			}
		}
		var fresh []Issue
		for _, i := range now.Issues {
			if !known[i] {
				fresh = append(fresh, i)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		return &ValidationError{Issues: fresh}
	})
}

func (s *Service) save(b match.Bank, expected string, validate func(match.Bank) error) (workspace.Saved, error) {
	if err := validate(b); err != nil {
		return workspace.Saved{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.ws.Read(workspace.FileAnswerBank)
	if err != nil {
		return workspace.Saved{}, err
	}
	if expected == "" {
		expected = cur.Revision
	}
	disk, derr := Parse(workspace.FileAnswerBank.Name(), cur.Data)
	if derr == nil {
		b = asWritten(unstamp(b, disk.Bank), disk, cur.Data)
	} else {
		b = unstamp(b, match.Bank{})
	}
	b = answeredNotPlaceholder(b)
	data, err := mergeBank(cur.Data, b)
	if err != nil {
		return workspace.Saved{}, err
	}
	if len(data) > MaxBankBytes {
		return workspace.Saved{}, &workspace.Error{Code: workspace.CodePayloadTooLarge, Path: workspace.FileAnswerBank.Name(),
			Message: fmt.Sprintf("the bank is limited to %d MiB", MaxBankBytes>>20)}
	}
	return s.ws.Write(workspace.FileAnswerBank, data, expected)
}

// answeredNotPlaceholder clears the placeholder flag of every entry that holds
// an answer: the flag only lets an entry stay blank, and an answered entry
// still marked placeholder is passed over as evidence and as a knowledge
// fact. Whichever writer gave the answer (the setup step, the Answer Bank
// page, an approved candidate), the saved entry is a real one. The ask-me
// marker is not an answer, so an entry left to the user keeps its flag.
func answeredNotPlaceholder(b match.Bank) match.Bank {
	answered := func(e match.Entry) bool { return e.Placeholder && !e.BodyEmpty() && !e.AsksMe() }
	if !slices.ContainsFunc(b.Entries, answered) {
		return b
	}
	out := b
	out.Entries = slices.Clone(b.Entries)
	for i := range out.Entries {
		if answered(out.Entries[i]) {
			out.Entries[i].Placeholder = false
		}
	}
	return out
}

// unstamp gives every entry whose body the load stamped the body it has in
// disk, the file as parsed, drops the entries the load created and blanks a
// currency the load gave.
func unstamp(b match.Bank, disk match.Bank) match.Bank {
	if !slices.ContainsFunc(b.Entries, func(e match.Entry) bool { return e.StampedBody || e.StampedCurrency }) {
		return b
	}
	out := b
	out.Entries = nil
	for _, e := range b.Entries {
		switch {
		case e.StampedBody:
			orig, ok := disk.Find(e.ID)
			if !ok {
				continue
			}
			e = orig
		case e.StampedCurrency:
			e.Currency, e.StampedCurrency = "", false
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// asWritten is b as the file takes it back: an entry the shipped defaults
// changed at load (withDefaults) that is still as the load gave it is written
// as the file holds it, or left out when the file lacks it. A save so writes
// what its writer changed, and that entry is the user's from then on; the
// defaults themselves never reach the file. disk is onDisk as parsed.
func asWritten(b match.Bank, disk Result, onDisk []byte) match.Bank {
	eff, changed, err := withDefaults(disk, onDisk)
	if err != nil || len(changed) == 0 {
		return b
	}
	out := b
	out.Entries = nil
	for _, e := range b.Entries {
		if d, ok := eff.Bank.Find(e.ID); changed[e.ID] && ok && sameEntry(e, d) {
			if f, ok := disk.Bank.Find(e.ID); ok {
				out.Entries = append(out.Entries, f)
			}
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// sameEntry reports whether two entries write the same fields to the file.
func sameEntry(a, b match.Entry) bool {
	na, err := encodeEntry(a)
	if err != nil {
		return false
	}
	nb, err := encodeEntry(b)
	return err == nil && sameContent(na, nb)
}

// Evolve moves every active, unpinned entry of the bank File reads with enough
// counted corrections to stale, editing only those entries in place; a shipped
// default the file lacks is appended in its new state. It returns the ids that
// changed. Nothing here archives an entry.
func (s *Service) Evolve(counts map[string]int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.ws.Read(workspace.FileAnswerBank)
	if err != nil {
		return nil, err
	}
	if cur.Revision == workspace.RevisionAbsent {
		return nil, nil
	}
	res, err := Parse(workspace.FileAnswerBank.Name(), cur.Data)
	if err == nil {
		res, _, err = withDefaults(res, cur.Data)
	}
	if err != nil {
		return nil, err
	}
	next, changed := res.Bank.Stale(counts, s.now().UTC().Format(time.RFC3339))
	if len(changed) == 0 {
		return nil, nil
	}
	index, err := entryIndex(cur.Data)
	if err != nil {
		return nil, err
	}
	var ops []workspace.Op
	var shipped []match.Entry
	for _, id := range changed {
		e, _ := next.Find(id)
		at, ok := index[id]
		if !ok {
			shipped = append(shipped, e)
			continue
		}
		ops = append(ops,
			setOp(fmt.Sprintf("entries[%d].state", at), e.State),
			setOp(fmt.Sprintf("entries[%d].stale_at", at), e.StaleAt),
			workspace.Op{Kind: workspace.OpSet, Path: fmt.Sprintf("entries[%d].stale_correction_count", at), Value: fmt.Sprint(e.StaleCorrectionCount)})
	}
	if len(shipped) > 0 {
		_, shape, err := fileEntryIDs(cur.Data)
		if err != nil {
			return nil, err
		}
		appended, err := appendOps(shipped, shape)
		if err != nil {
			return nil, err
		}
		ops = append(ops, appended...)
	}
	if _, err := s.ws.Edit(workspace.FileAnswerBank, ops, cur.Revision); err != nil {
		return nil, err
	}
	return changed, nil
}

// SecretKey returns the workspace's random key stored under name in the state
// directory, creating it on first use.
func (s *Service) SecretKey(name string) ([]byte, error) { return s.ws.SecretKey(name) }

// EntryIDs returns the ids of the user's bank and of the defaults bank. A bank
// that does not load reads as the ids it did load: an id is then unknown
// rather than guessed.
func (s *Service) EntryIDs() map[string]bool {
	ids := map[string]bool{}
	l, _ := s.File()
	def, _ := DefaultBank()
	for _, e := range slices.Concat(l.Bank.Entries, def.Bank.Entries) {
		ids[e.ID] = true
	}
	return ids
}

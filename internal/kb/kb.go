// Package kb is the knowledge base: Markdown snippets under knowledge/, the
// SQLite index over them, company briefs, lexical and optional semantic
// evidence retrieval, drafts and publishing, and the behavioral story bank.
// The files hold the truth; every index table can be rebuilt from them.
package kb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"applysmith/internal/flight"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// KnowledgeDir is the directory below the workspace root that holds snippets.
const KnowledgeDir = "knowledge"

// CompaniesDir holds company briefs, which are not snippets.
const CompaniesDir = "companies"

// Typed failures. Callers branch with errors.Is.
var (
	ErrInvalid     = errors.New("kb: invalid input")
	ErrNotFound    = errors.New("kb: not found")
	ErrConflict    = errors.New("kb: conflict")
	ErrUnavailable = errors.New("kb: unavailable")
	// ErrStale is the conflict of an edit made against a file that changed
	// since the editor opened it.
	ErrStale = fmt.Errorf("%w: changed since it was opened", ErrConflict)
)

// Embedder turns texts into vectors. The caller wires it to the routed
// embedding task; it returns the id of the model that served the call.
type Embedder interface {
	Embed(ctx context.Context, texts []string) (vectors [][]float32, model string, err error)
}

// Summarizer optionally condenses a company brief under the research-search
// task. A failure leaves the extracted text as it is.
type Summarizer interface {
	Summarize(ctx context.Context, text string) (string, error)
}

// Options configures a Service. Only Workspace is required.
type Options struct {
	Workspace *workspace.Workspace
	Now       func() time.Time
	// Embedder turns the semantic stage on. Nil keeps retrieval lexical and
	// sends nothing to any provider.
	Embedder Embedder
	// SemanticEnabled is checked per search; nil permits a supplied embedder.
	SemanticEnabled func(context.Context) bool
	// Fetcher reads web pages for company briefs. Nil makes brief building
	// report ErrUnavailable.
	Fetcher    Fetcher
	Summarizer Summarizer
	// Sources adds evidence sources that live in other packages (curated
	// answers, corrections).
	Sources []Source
	// Model is the routed story-extraction model: it drafts knowledge from a
	// repository and pre-fills a mining session from notes. Nil switches those
	// steps off.
	Model Model
	// GitHubToken returns the optional token for repository access. It is
	// read for each ingestion and never stored; an empty result means
	// unauthenticated access to public repositories.
	GitHubToken func(ctx context.Context) string
	// PDFText extracts the text of an imported PDF; nil refuses PDF imports.
	PDFText func(context.Context, []byte) (string, error)
}

// Service is the knowledge base of one workspace.
type Service struct {
	st   *store.Store
	ws   *workspace.Workspace
	opts Options
	now  func() time.Time

	// mu serialises index maintenance and every write to the knowledge files.
	mu sync.Mutex

	flights flight.Group
	memo    memoTable
	trash   trash

	// Test seams for repository ingestion: the API address, whether plain http
	// is accepted for it, the backoff base and the clone command.
	ghBase     string
	ghInsecure bool
	ghBackoff  time.Duration
	cloneSeam  *cloner
}

// New returns a Service over an open store.
func New(st *store.Store, opts Options) (*Service, error) {
	if st == nil || opts.Workspace == nil {
		return nil, errors.Join(ErrInvalid, errors.New("a store and a workspace are required"))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{st: st, ws: opts.Workspace, opts: opts, now: opts.Now}, nil
}

func (s *Service) stamp() string { return s.now().UTC().Format(time.RFC3339) }

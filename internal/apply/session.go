package apply

import (
	"context"
	"errors"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// Filler is the engine's page surface: one fill per call, a read-back of the
// controls, and the engine's own writer for a value the chain chose.
type Filler interface {
	Fill(ctx context.Context, o engine.FillOptions) (*engine.PageResult, error)
	Inspect(ctx context.Context) ([]engine.InspectRow, error)
	Answer(ctx context.Context, runID string, res []engine.Resolution) ([]engine.AnswerResult, error)
	ObserveSubmissions(ctx context.Context) error
	SubmitObservations(ctx context.Context, since time.Duration) ([]engine.SubmitObservation, error)
	BlockedSubmits(ctx context.Context) ([]engine.BlockedSubmit, error)
}

// FrameFiller is a Filler that can also read and write the document of one
// frame, for an application embedded in the page. The engine's page is one.
type FrameFiller interface {
	InspectIn(ctx context.Context, frame string) ([]engine.InspectRow, error)
	AnswerIn(ctx context.Context, frame, runID string, res []engine.Resolution) ([]engine.AnswerResult, error)
	BlockedSubmitsIn(ctx context.Context, frame string) ([]engine.BlockedSubmit, error)
}

var errNoFrameFiller = errors.New("the page surface cannot reach a frame")

// inputHolder brackets trusted input the run drives itself, the computer-use
// rung's: until the release, the engine of the document holds native
// submissions and takes no event for the person's edit. The release names the
// values the input left, which the engine's watch then counts as the run's.
type inputHolder interface {
	HoldInputIn(ctx context.Context, frame string) error
	ReleaseInputIn(ctx context.Context, frame string, writes []engine.Resolution) error
}

var _ inputHolder = (*engine.Page)(nil)

// holderOf is the engine's input bracket behind the page surface, when it has
// one: a page without it never gets the rung's input.
func holderOf(p Filler) (inputHolder, bool) {
	if lf, ok := p.(*loggedFiller); ok {
		p = lf.Filler
	}
	h, ok := p.(inputHolder)
	return h, ok
}

// framer is the page surface for the frame the application lives in.
func (s *session) framer() (FrameFiller, error) {
	ff, ok := s.page.(FrameFiller)
	if !ok {
		return nil, errNoFrameFiller
	}
	return ff, nil
}

// ResponseWatch observes the answers a tab receives during the submit window.
type ResponseWatch interface {
	Responses() []browser.Response
	Pending() int
	Stop()
}

// Tab is the browser surface one run drives. Pointer and key input is trusted
// input of the browser layer, never synthetic page events.
type Tab interface {
	ID() string
	Navigate(ctx context.Context, url string) (browser.NavResult, error)
	// NavigateDOM returns once the document is parsed, not when it has loaded:
	// for a single-page application whose load event may never come.
	NavigateDOM(ctx context.Context, url string) (browser.NavResult, error)
	// Frames lists the tab's frames, the top frame first; EvalFrame runs an
	// expression in one of them and decodes its JSON result into out.
	Frames(ctx context.Context) ([]browser.Frame, error)
	EvalFrame(ctx context.Context, frame, expr string, out any) error
	// Eval runs an expression in the page's main world and decodes the JSON
	// result into out (nil discards it).
	Eval(ctx context.Context, expr string, out any) error
	// Inventory reads the fields of the document in frame ("" is the top one)
	// from the engine's inspect result; forms are the page's visible forms.
	Inventory(ctx context.Context, frame string, forms []FormInfo) (Inventory, error)
	Click(ctx context.Context, x, y float64) error
	TypeText(ctx context.Context, text string) error
	// PressKey presses one named key (Tab) on the focused element.
	PressKey(ctx context.Context, key string) error
	Screenshot(ctx context.Context) ([]byte, error)
	WatchResponses(ctx context.Context) (ResponseWatch, error)
	// Keep leaves the tab open for the user when the run ends.
	Keep() error
	Close(ctx context.Context) error
	Page() Filler
}

// Window is the apply window of the managed browser.
type Window interface {
	OpenTab(ctx context.Context, owner string, h engine.Host) (Tab, error)
}

// inspect, answer and blocked reach the document the page's application lives
// in: the top document, or the frame the engine found it in.
func (s *session) inspect(ctx context.Context) ([]engine.InspectRow, error) {
	return s.inspectDoc(ctx, s.frame)
}

// inspectDoc reads one document of the page: a frame's, or the top one ("").
func (s *session) inspectDoc(ctx context.Context, doc string) ([]engine.InspectRow, error) {
	if doc == "" {
		return s.page.Inspect(ctx)
	}
	ff, err := s.framer()
	if err != nil {
		return nil, err
	}
	return ff.InspectIn(ctx, doc)
}

func (s *session) answer(ctx context.Context, runID string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	res, skipped := s.keep.filter(s.frame, res)
	if len(res) == 0 {
		return skipped, nil
	}
	if s.frame == "" {
		got, err := s.page.Answer(ctx, runID, res)
		return append(got, skipped...), err
	}
	ff, err := s.framer()
	if err != nil {
		return nil, err
	}
	got, err := ff.AnswerIn(ctx, s.frame, runID, res)
	return append(got, skipped...), err
}

func (s *session) blocked(ctx context.Context) ([]engine.BlockedSubmit, error) {
	if s.frame == "" {
		return s.page.BlockedSubmits(ctx)
	}
	ff, err := s.framer()
	if err != nil {
		return nil, err
	}
	return ff.BlockedSubmitsIn(ctx, s.frame)
}

// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"applysmith/internal/demo"
	"applysmith/internal/workspace"
)

type failingReader struct{ calls int }

func (r *failingReader) Read(context.Context, string) (PageRead, error) {
	r.calls++
	return PageRead{}, errors.New("no network in this test")
}

// The sample postings sit on reserved domains that never resolve: a demo
// workspace reads them from the shipped fixtures, and nothing else does.
func TestDemo_TheSamplePostingsAreReadFromTheFixturesInADemoOnly(t *testing.T) {
	e := newTenv(t)
	inner := &failingReader{}
	r := demoReader{st: e.st, inner: inner}
	p := demo.Postings()[0]
	if _, err := r.Read(context.Background(), p.URL); err == nil || inner.calls != 1 {
		t.Fatalf("outside a demo the fixture was served (err %v, inner calls %d)", err, inner.calls)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.st.Path()), workspace.DemoMarkerName), []byte("demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := r.Read(context.Background(), p.URL)
	if err != nil || got.Text != p.Description || got.Company != p.Company || got.Title != p.Title || got.Result != "unverified" {
		t.Fatalf("the demo read %+v (%v), want the fixture marked unverified", got, err)
	}
	if _, err := r.Read(context.Background(), "https://jobs.example.test/other"); err == nil || inner.calls != 2 {
		t.Fatal("a URL that is not a sample posting did not go to the real reader")
	}
}

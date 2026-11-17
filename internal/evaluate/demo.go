// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"

	"applysmith/internal/demo"
	"applysmith/internal/store"
)

// demoReader serves the demo's sample postings from the shipped fixtures in a
// demo workspace: their reserved domains never resolve, and the tutorial
// evaluates them with no network. A fixture is not a live read, so it is
// marked unverified. Any other URL, and every URL outside a demo, goes to the
// real reader.
type demoReader struct {
	st    *store.Store
	inner Reader
}

// Read implements Reader.
func (r demoReader) Read(ctx context.Context, url string) (PageRead, error) {
	if r.st.Demo() {
		if p, ok := demo.PostingAt(url); ok {
			return PageRead{Title: p.Title, Company: p.Company, Location: p.Location, Compensation: p.Compensation, Text: p.Description,
				Method: "fixture", Result: "unverified"}, nil
		}
	}
	return r.inner.Read(ctx, url)
}

package gmail

import (
	"database/sql"
	"fmt"
	"testing"

	"applysmith/internal/tracker"
)

// A link pass re-decides every unlinked signal on each sync. The scoring is
// the slow part of it, and the database has one writer for the whole app, so
// the pass must not hold that writer while it scores: a pass that finds nothing
// to change completes while another writer holds the database.
func TestGmail_Resolve_LeavesTheWriterFreeWhileScoring(t *testing.T) {
	p := newPipeEnv(t)
	var sigs []sig
	for i := range 30 {
		sigs = append(sigs, sig{id: fmt.Sprintf("m%03d", i), typ: "applied", employer: fmt.Sprintf("Acme%03d Labs", i), role: fmt.Sprintf("Quality Lead %d", i)})
	}
	p.seed(sigs...)
	for i := range 30 {
		p.add(fmt.Sprintf("Zeta%03d Works", i), fmt.Sprintf("Platform Analyst %d", i), tracker.Applied)
	}
	pl := p.stack.Pipeline
	// The first pass records every decision; the second scores them again and
	// finds nothing to change.
	if n, err := pl.Resolve(p.ctx); err != nil || n != 0 {
		t.Fatalf("first pass linked %d: %v", n, err)
	}

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- p.st.Write(p.ctx, func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	_, err := pl.Resolve(p.ctx)
	close(release)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("a pass with nothing to write needed the writer while scoring: %v", err)
	}
}

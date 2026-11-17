// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"errors"

	"applysmith/internal/store"
)

// Claim scopes an Idempotency-Key: the authenticated caller and the key it
// sent. The operation is fixed, so one key names one evaluation request.
type Claim struct {
	Principal string
	Key       string
}

const claimOperation = "evaluate"

// SubmitKeyed is Submit for a request that came with an Idempotency-Key. The
// claim, the run and its job row are stored in one transaction before the job is queued, so
// a retry after a lost response, or after a restart, receives the original
// run: Handle.Replayed is set and nothing is queued again. The same key with
// different input is store.ErrConflict. A request that joins a job already
// active for the posting binds the key to that job, so its retry joins it too.
func (p *Pool) SubmitKeyed(ctx context.Context, c Claim, r Request) (Handle, error) {
	r, key, err := prepare(r)
	if err != nil {
		return Handle{}, err
	}
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	p.mu.Lock()
	active, joined := p.inflight[key]
	started, full := p.started, len(p.user)+len(p.scan) >= p.opts.QueueMax
	p.mu.Unlock()
	switch {
	case !started:
		return Handle{}, errors.New("evaluate: the pool is not running")
	case !joined && full:
		return Handle{}, ErrQueueFull
	}
	req := store.RunRequest{Principal: c.Principal, Operation: claimOperation, Key: c.Key}
	if joined {
		req.Attach = active.runID
	}
	// The claim, the run and the job row commit together, and enqueueing is
	// in memory, so nothing after the claim can fail: a retry that replays the
	// claim always receives a run that will execute.
	ref, err := p.ev.claimRun(ctx, req, r, key)
	switch {
	case err != nil:
		return Handle{}, err
	case ref.Replayed:
		return Handle{RunID: ref.RunID, Replayed: true}, nil
	case joined:
		p.attachOrigin(active, r)
		return Handle{RunID: active.runID, Attached: true}, nil
	}
	p.enqueue(ref.RunID, key, r)
	return Handle{RunID: ref.RunID}, nil
}

// attachOrigin follows the database claim of a keyed join. The job may have
// ended during that claim; in that case settle the new origin from its saved
// result, after its existing settlement has completed.
func (p *Pool) attachOrigin(job *pending, req Request) {
	p.mu.Lock()
	o := originOf(req)
	if o == (outcomeOrigin{}) || job.seen[o] {
		p.mu.Unlock()
		return
	}
	if p.inflight[job.key] == job {
		job.addOrigin(req)
		p.mu.Unlock()
		return
	}
	job.seen[o] = true
	p.mu.Unlock()
	<-job.done
	p.settleOrigin(job.runID, o, job.res)
}

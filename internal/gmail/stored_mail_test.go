package gmail

import (
	"errors"
	"testing"

	"applysmith/internal/gmail/classify"
)

func TestStoredMail_ReadsTheSenderSubjectAndBodyAndFallsBackToTheSnippet(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeReplyNeeded, subject: "Quick chat", body: "Can you talk on Thursday?"})
	m, err := p.repo.StoredMail(p.ctx, "m1")
	if err != nil || m.Subject != "Quick chat" || m.Body != "Can you talk on Thursday?" || m.From == "" {
		t.Fatalf("mail = %+v %v", m, err)
	}
	// The retention sweep removed the body: the snippet is all that is left.
	if _, err := p.repo.Sweep(p.ctx, 0); err != nil {
		t.Fatal(err)
	}
	m, err = p.repo.StoredMail(p.ctx, "m1")
	if err != nil || m.Body != "snippet of m1" {
		t.Fatalf("after the sweep: %+v %v", m, err)
	}
	if _, err := p.repo.StoredMail(p.ctx, "none"); !errors.Is(err, ErrNoMail) {
		t.Fatalf("unknown message: %v", err)
	}
}

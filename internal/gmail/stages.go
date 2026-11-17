package gmail

import (
	"context"
	"strings"
	"time"

	"applysmith/internal/interview"
)

// StageProposer records an interview stage. A mail only proposes one, so the
// stage it creates is never confirmed.
type StageProposer interface {
	RecordStage(ctx context.Context, candidacy int64, in interview.StageInput) (interview.Stage, error)
}

// stageTypeOf reads the kind of interview a mail announces from its wording;
// anything it does not name is "other".
func stageTypeOf(text string) string {
	t := strings.ToLower(text)
	for _, c := range []struct{ phrase, typ string }{
		{"system design", interview.TypeSystemDesign}, {"phone screen", interview.TypePhoneScreen}, {"technical", interview.TypeTechnical},
		{"coding", interview.TypeTechnical}, {"behavioral", interview.TypeBehavioral}, {"onsite", interview.TypeOnsite},
		{"on-site", interview.TypeOnsite}, {"final", interview.TypeFinal},
	} {
		if strings.Contains(t, c.phrase) {
			return c.typ
		}
	}
	return interview.TypeOther
}

// stageIDOf derives a stable stage id from the message, so the same mail
// proposes the same stage.
func stageIDOf(messageID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(messageID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	id := "mail-" + b.String()
	if len(id) > 40 {
		id = id[:40]
	}
	return id
}

// proposeStage records the unconfirmed stage an interview mail announces. A
// failure is not the outcome's failure: the status change stands.
func (p *Pipeline) proposeStage(ctx context.Context, candidacy int64, s SignalRow) {
	if p.Stages == nil || len(stageIDOf(s.MessageID)) < 7 {
		return
	}
	in := interview.StageInput{ID: stageIDOf(s.MessageID), Type: stageTypeOf(s.Subject + " " + s.Snippet), Format: "unknown"}
	if s.DueMS > 0 {
		in.At = time.UnixMilli(s.DueMS).UTC().Format(time.RFC3339)
	}
	_, _ = p.Stages.RecordStage(ctx, candidacy, in)
}

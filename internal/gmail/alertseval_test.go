package gmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
)

var errDeliver = errors.New("delivery failed")

func TestAlerts_AssessmentAlertsOnceAcrossSyncs(t *testing.T) {
	p := newPipeEnv(t)
	// The first evaluation after the connection seeds silently.
	if n, err := p.stack.EvaluateAlerts(p.ctx); err != nil || n != 0 {
		t.Fatalf("seed = %d %v", n, err)
	}
	p.seed(sig{id: "a1", thread: "t1", typ: classify.TypeAssessment, role: "Backend Engineer", at: p.ago(2 * hour)})
	for range 3 {
		if _, err := p.stack.EvaluateAlerts(p.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.alerts) != 1 || !strings.Contains(p.alerts[0].Title, "online assessment") {
		t.Fatalf("alerts = %+v", p.alerts)
	}
}

func TestAlerts_FirstEvaluationSeedsSilently(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "i1", thread: "t1", typ: classify.TypeInterview, at: p.ago(hour), due: p.clk.now().Add(30 * hour)})
	if n, _ := p.stack.EvaluateAlerts(p.ctx); n != 0 || len(p.alerts) != 0 {
		t.Fatalf("the first evaluation alerted: %+v", p.alerts)
	}
	var state string
	if err := p.st.Read().QueryRow(`SELECT state FROM gmail_alert_states WHERE thread_id = 't1'`).Scan(&state); err != nil || state == "" {
		t.Fatalf("state = %q %v", state, err)
	}
}

func TestAlerts_StateAndEligibility(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     sig
		alert bool
	}{
		{"reply needed", sig{typ: classify.TypeReplyNeeded}, true},
		{"action required", sig{typ: classify.TypeActionNeeded}, true},
		{"offer", sig{typ: classify.TypeOffer}, true},
		{"interview ahead", sig{typ: classify.TypeInterview, due: time.Date(2027, 3, 12, 15, 0, 0, 0, time.UTC)}, true},
		{"reminder", sig{typ: classify.TypeReminder, due: time.Date(2027, 3, 10, 15, 0, 0, 0, time.UTC)}, true},
		{"receipt never", sig{typ: classify.TypeApplied}, false},
		{"rejection never", sig{typ: classify.TypeRejection}, false},
		{"review tier never", sig{typ: classify.TypeReplyNeeded, tier: classify.TierReview, reasons: classify.ReviewCompanyUnknown}, false},
		{"low priority never", sig{typ: classify.TypeReplyNeeded, tier: classify.TierLow}, false},
		{"suppressed never", sig{typ: classify.TypeReplyNeeded, tier: classify.TierSuppressed}, false},
		{"outgoing never", sig{typ: classify.TypeReplyNeeded, dir: classify.DirOutgoing}, false},
		{"assessment submitted waits", sig{typ: classify.TypeAssessment, rule: classify.RuleAssessmentSubmitted}, false},
	} {
		p := newPipeEnv(t)
		_, _ = p.stack.EvaluateAlerts(p.ctx)
		c.s.id, c.s.thread, c.s.at = "m", "t", p.ago(hour)
		p.seed(c.s)
		if _, err := p.stack.EvaluateAlerts(p.ctx); err != nil {
			t.Fatal(err)
		}
		if got := len(p.alerts) == 1; got != c.alert {
			t.Errorf("%s: alerted %v, want %v (%+v)", c.name, got, c.alert, p.alerts)
		}
	}
}

func TestAlerts_FreshWithin24HoursIncomingOnly(t *testing.T) {
	p := newPipeEnv(t)
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	// Incoming mail 25 hours old does not alert even as the thread enters the lane now.
	p.seed(sig{id: "old", thread: "t1", typ: classify.TypeReplyNeeded, at: p.ago(25 * hour)})
	// The user's own outgoing mail is fresh, but is not fresh incoming mail.
	p.seed(sig{id: "mine", thread: "t1", typ: classify.TypeReplyNeeded, dir: classify.DirOutgoing, at: p.ago(time.Minute)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 0 {
		t.Fatalf("alerts = %+v", p.alerts)
	}
	p.seed(sig{id: "new", thread: "t2", typ: classify.TypeReplyNeeded, at: p.ago(23 * hour)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 1 {
		t.Fatalf("alerts = %+v", p.alerts)
	}
}

func TestAlerts_KeyedByThreadIDNotCandidacy(t *testing.T) {
	p := newPipeEnv(t)
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, at: p.ago(hour)}, sig{id: "b", thread: "t2", typ: classify.TypeReplyNeeded, at: p.ago(hour)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 2 || p.alerts[0].MessageID == p.alerts[1].MessageID {
		t.Fatalf("two threads of one company must alert separately: %+v", p.alerts)
	}
}

func TestAlerts_RecordBeforeDeliverSeedAndOff(t *testing.T) {
	p := newPipeEnv(t)
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	// Alerts off: the state is recorded and nothing is raised, now or after switching on.
	off := false
	if err := p.repo.SetSwitches(p.ctx, &off, nil); err != nil {
		t.Fatal(err)
	}
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeAssessment, at: p.ago(hour)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	on := true
	_ = p.repo.SetSwitches(p.ctx, &on, nil)
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 0 {
		t.Fatalf("alerts after switching on = %+v", p.alerts)
	}
	// A crash in delivery never alerts twice: the state is written first.
	p.stack.Alerts = func(context.Context, Alert) error { return errDeliver }
	p.seed(sig{id: "b", thread: "t2", typ: classify.TypeReplyNeeded, at: p.ago(hour)})
	if n, err := p.stack.EvaluateAlerts(p.ctx); err == nil || n != 0 {
		t.Fatalf("a failing delivery = %d %v", n, err)
	}
	p.stack.Alerts = func(_ context.Context, a Alert) error { p.alerts = append(p.alerts, a); return nil }
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 0 {
		t.Fatalf("a recorded state alerted again: %+v", p.alerts)
	}
	// A thread that leaves the alert state and returns alerts again.
	p.clk.advance(time.Minute)
	p.seed(sig{id: "r", thread: "t2", typ: classify.TypeReplyNeeded, dir: classify.DirOutgoing, at: p.clk.now()})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	p.clk.advance(time.Minute)
	p.seed(sig{id: "again", thread: "t2", typ: classify.TypeReplyNeeded, at: p.clk.now()})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 1 || !strings.HasPrefix(p.alerts[0].MessageID, "t2@") {
		t.Fatalf("alerts = %+v", p.alerts)
	}
}

func TestAlerts_RejectionNeverAlertsAndOfferStatesOnlyTheOutcome(t *testing.T) {
	p := newPipeEnv(t)
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	p.seed(sig{id: "rj", thread: "t1", typ: classify.TypeRejection, at: p.ago(hour)},
		sig{id: "of", thread: "t2", typ: classify.TypeOffer, role: "Backend Engineer", at: p.ago(hour), due: p.clk.now().Add(2 * day)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 1 || !strings.Contains(p.alerts[0].Title, "offer received") {
		t.Fatalf("alerts = %+v", p.alerts)
	}
	if strings.Contains(strings.ToLower(p.alerts[0].Body), "due") || !p.alerts[0].InterviewStart.IsZero() {
		t.Errorf("an offer alert states a next step: %+v", p.alerts[0])
	}
}

func TestAlerts_NothingBeforeTheFirstSyncCompletes(t *testing.T) {
	p := newPipeEnv(t)
	if err := p.repo.SetState(p.ctx, map[string]string{keyCursor: "", keyCursorUpdated: ""}); err != nil {
		t.Fatal(err)
	}
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, at: p.ago(hour)})
	if n, _ := p.stack.EvaluateAlerts(p.ctx); n != 0 {
		t.Fatal("alerted before a sync finished")
	}
	if p.repo.stateOr(p.ctx, keyAlertsSeed) != "" {
		t.Error("the seed was taken before a sync finished")
	}
}

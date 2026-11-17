package classify

import (
	"testing"
	"time"
)

func addr(local, domain string) string { return local + "@" + domain }

func from(name, local, domain string) string {
	if name == "" {
		return addr(local, domain)
	}
	return name + " <" + addr(local, domain) + ">"
}

var refTime = time.Date(2027, 3, 10, 15, 0, 0, 0, time.UTC)

func newClassifier() *Classifier { return &Classifier{Tax: Defaults(), WindowsKnown: true} }

// classify runs one synthetic message through a default classifier.
func classify(t *testing.T, in Input) Result {
	t.Helper()
	if in.MessageID == "" {
		in.MessageID = "m-test"
	}
	if in.ReceivedAt.IsZero() {
		in.ReceivedAt = refTime
	}
	if in.Raw == "" {
		in.Raw = in.Text
	}
	return newClassifier().Classify(in)
}

func mustSignal(t *testing.T, r Result) *Signal {
	t.Helper()
	if r.Signal == nil {
		t.Fatalf("message ignored: %s (candidates %+v)", r.Reason, r.Candidates)
	}
	return r.Signal
}

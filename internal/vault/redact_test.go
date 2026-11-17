package vault

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestRedactReplacesAReadOrStoredValueInEveryForm(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	stored := "Stored-Redact-1\"q\\z"
	mustSet(t, v, "api-key-openrouter", stored)
	got, err := v.Get("api-key-openrouter")
	if err != nil || got != stored {
		t.Fatal(got, err)
	}
	enc, _ := json.Marshal(stored)
	for name, in := range map[string]string{
		"raw":  "failed with " + stored + " done",
		"json": "body " + string(enc) + " done",
	} {
		out := Redact(in)
		if strings.Contains(out, "Stored-Redact-1") || !strings.Contains(out, "[redacted]") {
			t.Errorf("%s: %q", name, out)
		}
	}
}

func TestRedactSeesAnEnvironmentOverride(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), map[string]string{EnvName("telegram-bot-token"): "env-redact-token-77"})
	if _, err := v.Get("telegram-bot-token"); err != nil {
		t.Fatal(err)
	}
	if out := Redact("x env-redact-token-77 y"); strings.Contains(out, "env-redact") {
		t.Fatal(out)
	}
}

// A value refused as too large was still offered as a secret, so a log line
// that echoes it is scrubbed.
func TestRedactCoversAValueRefusedAsTooLarge(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	big := "too-large-secret-" + strings.Repeat("x", maxValueBytes)
	if _, err := v.Set("api-key-openrouter", big); err == nil {
		t.Fatal("a value over the limit was stored")
	}
	if out := Redact("rejected " + big); strings.Contains(out, "too-large-secret") {
		t.Fatalf("the refused value reached the log: %.60q", out)
	}
}

func TestRedactIgnoresValuesThatWereNeverRead(t *testing.T) {
	if out := Redact("never-seen-value-9"); out != "never-seen-value-9" {
		t.Fatal(out)
	}
}

func TestRedactWriterScrubsALogLineAndReportsTheFullLength(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	mustSet(t, v, "api-key-openrouter", "writer-redact-secret-5")
	var sink bytes.Buffer
	line := []byte("level=INFO msg=oops key=writer-redact-secret-5\n")
	n, err := RedactWriter{W: &sink}.Write(line)
	if err != nil || n != len(line) || strings.Contains(sink.String(), "writer-redact") {
		t.Fatalf("%d %v %q", n, err, sink.String())
	}
}

func TestRedactorForgetsTheOldestValuesWhenFull(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	for i := range maxRemembered + 10 {
		mustSet(t, v, "api-key-openrouter", "bounded-redact-"+strconv.Itoa(i))
	}
	redactor.mu.RLock()
	n := len(redactor.order)
	redactor.mu.RUnlock()
	if n > maxRemembered {
		t.Fatalf("%d values kept", n)
	}
}

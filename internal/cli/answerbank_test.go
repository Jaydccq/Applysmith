package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAnswerBankStatusOfAWorkspaceWithoutABank(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "answer-bank", "status", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "no answer bank yet") || !strings.Contains(o.stdout, "0 entries") {
		t.Fatalf("%+v", o)
	}
}

func TestAnswerBankInitSeedsTheDefaultBankAndStatusCountsIt(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "created answer-bank.yml\n") {
		t.Fatalf("%+v", o)
	}
	o := te.run(context.Background(), "answer-bank", "status", "--workspace", te.ws, "--json")
	if o.code != 0 {
		t.Fatalf("%+v", o)
	}
	var st struct {
		Entries    int      `json:"entries"`
		Unanswered int      `json:"unanswered"`
		Blocking   []string `json:"blocking"`
		Warnings   []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(o.stdout), &st); err != nil {
		t.Fatal(err, o.stdout)
	}
	if st.Entries < 200 || st.Unanswered == 0 || len(st.Blocking) == 0 || len(st.Warnings) != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestAnswerBankUsage(t *testing.T) {
	te := newTestEnv(t)
	for _, args := range [][]string{{"answer-bank"}, {"answer-bank", "frobnicate"}} {
		if o := te.run(context.Background(), args...); o.code != 2 || !strings.Contains(o.stderr, "usage:") {
			t.Errorf("%v: %+v", args, o)
		}
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"applysmith/internal/onboarding"
)

// The agent lane (`setup status`, `setup next`, `setup schema`) lists the
// catalog's steps in the catalog's order with its titles and required flags,
// and every status equals the one the library computes.
func TestAgentLaneTakesItsStepsFromTheCatalog(t *testing.T) {
	te := newTestEnv(t)
	te.hook = machineProbes
	ctx := context.Background()
	te.run(ctx, "init", "--workspace", te.ws)
	st := te.status()
	cat := onboarding.Catalog()
	if len(st.Steps) != len(cat) {
		t.Fatalf("status lists %d steps, the catalog %d", len(st.Steps), len(cat))
	}
	for i, s := range st.Steps {
		c := cat[i]
		if s.ID != c.ID || s.N != c.N || s.Title != c.Title || s.Required != c.Required {
			t.Errorf("position %d: %+v, catalog %s/%d/%q/%v", i, s, c.ID, c.N, c.Title, c.Required)
		}
	}
	oe := te.env(&bytes.Buffer{}, &bytes.Buffer{}).onboardingEnv(te.ws)
	closeFn, err := oe.Open(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	for _, v := range onboarding.State(ctx, oe).Steps {
		if st.of(v.ID) != string(v.Status) {
			t.Errorf("%s: status lane %s, library %s", v.ID, st.of(v.ID), v.Status)
		}
	}
	next := te.run(ctx, "setup", "next", "--json", "--workspace", te.ws)
	var n onboarding.NextReport
	if err := json.Unmarshal([]byte(next.stdout), &n); err != nil || n.Step == nil {
		t.Fatalf("next = %+v %v", next, err)
	}
	first, _ := onboarding.FirstIncomplete(onboarding.EvaluateAll(ctx, oe))
	if n.Step.ID != first.ID || n.Step.Guide != first.Guide {
		t.Errorf("next names %s, the catalog's first incomplete step is %s", n.Step.ID, first.ID)
	}
	for _, c := range cat {
		out := te.run(ctx, "setup", "schema", c.ID, "--json")
		var fields []onboarding.Field
		if out.code != 0 || (out.stdout != "null\n" && json.Unmarshal([]byte(out.stdout), &fields) != nil) {
			t.Fatalf("schema %s: %+v", c.ID, out)
		}
		if len(fields) != len(c.Schema) {
			t.Errorf("schema %s lists %d fields, the catalog %d", c.ID, len(fields), len(c.Schema))
		}
	}
	if out := te.run(ctx, "setup", "schema", "frobnicate"); out.code != 2 {
		t.Errorf("a step outside the catalog was accepted: %+v", out)
	}
}

// checkStepsFromTheCatalog checks that the interactive flow, which printed
// stdout, announced each step with the catalog's number, title and count, in
// the catalog's order.
func checkStepsFromTheCatalog(t *testing.T, stdout string) {
	t.Helper()
	cat := onboarding.Catalog()
	announced := regexp.MustCompile(`(?m)^Step (\d+) of (\d+): (.*)$`).FindAllStringSubmatch(stdout, -1)
	if len(announced) == 0 {
		t.Fatalf("no step was announced:\n%s", stdout)
	}
	var ids []string
	for _, a := range announced {
		n, _ := strconv.Atoi(a[1])
		total, _ := strconv.Atoi(a[2])
		if n >= len(cat) || total != len(cat)-1 || a[3] != cat[n].Title || !cat[n].Required {
			t.Errorf("announced %q: not a required step of the catalog", a[0])
			continue
		}
		ids = append(ids, cat[n].ID)
	}
	// A step that was already complete is not announced. Orientation closes
	// the run so that it can list what was skipped.
	var want []string
	for _, c := range cat {
		if c.Required && c.Phase != onboarding.PhaseStart {
			want = append(want, c.ID)
		}
	}
	want = append(want, "orientation")
	at := 0
	for _, id := range ids {
		for at < len(want) && want[at] != id {
			at++
		}
		if at == len(want) {
			t.Fatalf("announced steps %v are not in the catalog's order %v", ids, want)
		}
		at++
	}
	for _, id := range []string{"story", "resumes", "answer-bank", "llm", "browser", "orientation"} {
		if !slices.Contains(ids, id) {
			t.Errorf("the flow never reached %s: %v", id, ids)
		}
	}
}

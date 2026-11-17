package agent

import (
	"strings"
	"testing"
	"time"
)

func validTool() Tool {
	return Tool{Name: "tracker_query", Label: "Reading", Description: "Reads.", Class: ClassRead, PII: PIILow, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"status":{"type":"string"}}}`, Run: noop}
}

func TestRegistry_RegistrationRulesRefuseAndNameTheTool(t *testing.T) {
	// The forbidden names and the missing metadata are the parity tests'
	// (TestAgent_Registry_ForbiddenAndSubmitWordNames, _RefusesToolWithoutMetadata).
	cases := []struct {
		name string
		mod  func(*Tool)
		want string
	}{
		{"upper case", func(t *Tool) { t.Name = "Tracker_Query" }, "must match"},
		{"too short", func(t *Tool) { t.Name = "x" }, "must match"},
		{"timeout past the API deadline", func(t *Tool) { t.Timeout = MaxToolTimeout + time.Second }, "timeout"},
		{"schema allows extra arguments", func(t *Tool) { t.Schema = `{"type":"object","properties":{}}` }, "additionalProperties"},
		{"schema is not JSON", func(t *Tool) { t.Schema = `{` }, "not JSON"},
		{"own step argument", func(t *Tool) {
			t.Schema = `{"type":"object","additionalProperties":false,"properties":{"step":{"type":"string"}}}`
		}, "step"},
		{"idempotency key on a read tool", func(t *Tool) { t.Idempotent = true }, "idempotency"},
		{"submit capability on another tool", func(t *Tool) {
			t.Schema = `{"type":"object","additionalProperties":false,"properties":{"submit":{"type":"boolean"}}}`
		}, "submit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := validTool()
			c.mod(&tool)
			_, err := NewRegistry(tool)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "tool \""+tool.Name+"\"") {
				t.Fatalf("the error does not name the tool: %v", err)
			}
		})
	}
	if _, err := NewRegistry(validTool(), validTool()); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := NewRegistry(validTool()); err != nil {
		t.Fatalf("a valid tool was refused: %v", err)
	}
}

func TestRegistry_OfferedAndSpecsFollowTheChannelEnvelope(t *testing.T) {
	f := newFixture(t, guardTools()...)
	names := func(ch Channel) string {
		var out []string
		for _, s := range f.registry.SpecsFor(&Turn{Channel: ch}) {
			out = append(out, s.Name)
		}
		return strings.Join(out, " ")
	}
	scheduled := names(ChannelScheduled)
	for _, bad := range []string{"save_fact", "evaluate_job", "scan_portal", "wipe_workspace"} {
		if strings.Contains(scheduled, bad) {
			t.Errorf("a scheduled turn is offered %s", bad)
		}
	}
	if !strings.Contains(scheduled, "propose_rule") || !strings.Contains(scheduled, "gmail_signals") {
		t.Errorf("a scheduled turn lacks read and draft tools: %s", scheduled)
	}
	if !strings.Contains(names(ChannelDashboard), "save_fact") {
		t.Error("the dashboard is not offered write tools")
	}
	plain := newFixture(t)
	for _, s := range plain.registry.SpecsFor(&Turn{Channel: ChannelDashboard}) {
		if isApplyTool(s.Name) {
			t.Errorf("%s has no executor and was offered to the model", s.Name)
		}
	}
	if _, ok := plain.registry.Lookup("autofill_application"); !ok {
		t.Error("the declared apply tool is not in the registry, so the guard chain could not decide its calls")
	}
	for _, s := range f.registry.SpecsFor(&Turn{Channel: ChannelDashboard}) {
		step, _ := s.Parameters["properties"].(map[string]any)["step"].(map[string]any)
		if step == nil || step["maxLength"] != nil {
			t.Errorf("%s: step missing or length-limited in the schema pi validates: %v", s.Name, step)
		}
	}
	// Adding step to the offered schema must not touch the registry's own copy.
	tool, _ := f.registry.Lookup("save_fact")
	if _, leaked := tool.params["properties"].(map[string]any)["step"]; leaked {
		t.Error("the registered schema was modified")
	}
}

func TestArguments_UnknownArgumentIsRejectedAndStepIsStripped(t *testing.T) {
	schema := map[string]any{}
	_ = jsonUnmarshal(`{"type":"object","additionalProperties":false,"required":["q"],"properties":{
		"q":{"type":"string","minLength":2,"maxLength":5},"n":{"type":"integer","minimum":1,"maximum":3},
		"f":{"type":"number"},"b":{"type":"boolean"},"e":{"type":"string","enum":["a","b"]},
		"l":{"type":"array","maxItems":2,"items":{"type":"string"}},
		"m":{"type":"array","minItems":1,"items":{"type":"string"}}}}`, &schema)
	for _, c := range []struct {
		args string
		ok   bool
	}{
		{`{"q":"abc"}`, true},
		{`{"q":"abc","n":2,"f":1.5,"b":true,"e":"a","l":["x"]}`, true},
		{`{"q":"abc","extra":1}`, false},
		{`{}`, false},
		{`{"q":"a"}`, false},
		{`{"q":"abcdef"}`, false},
		{`{"q":"abc","n":4}`, false},
		{`{"q":"abc","n":1.5}`, false},
		{`{"q":"abc","n":"2"}`, false},
		{`{"q":"abc","e":"c"}`, false},
		{`{"q":"abc","b":"yes"}`, false},
		{`{"q":"abc","l":["a","b","c"]}`, false},
		{`{"q":"abc","l":[1]}`, false},
		{`{"q":"abc","m":["x"]}`, true},
		{`{"q":"abc","m":[]}`, false},
		{`[]`, false},
		{`null`, false},
	} {
		args, _, err := splitStep([]byte(c.args))
		if err == nil {
			err = validateArgs(schema, args)
		}
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.args, err, c.ok)
		}
	}
	args, step, err := splitStep([]byte(`{"q":"abc","step":"Looking it up"}`))
	if err != nil || step != "Looking it up" || strings.Contains(string(args), "step") || validateArgs(schema, args) != nil {
		t.Fatalf("split = %s %q %v", args, step, err)
	}
	if a, b := canonicalArgs([]byte(`{"b":1,"a":2.50}`)), canonicalArgs([]byte(`{"a":2.50,"b":1}`)); a != b || !strings.Contains(a, "2.50") {
		t.Fatalf("canonical = %s / %s", a, b)
	}
}

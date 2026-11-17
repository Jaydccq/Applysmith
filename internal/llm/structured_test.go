package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const personSchema = `{"type":"object","required":["name","age"],"properties":{"name":{"type":"string","minLength":1},"age":{"type":"integer","minimum":0},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`

type person struct {
	Name string
	Age  int
}

func seqHandler(replies ...string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		oaStream(w, "stop", 10, 5, 0, replies[min(n, len(replies))-1])
	}
}

func TestLLM_Structured_ValidOnFirstTry(t *testing.T) {
	e, sa, _ := pair(t, seqHandler("```json\n{\"name\":\"Ada Example\",\"age\":36}\n```"), okHandler("x"))
	var p person
	if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &p); err != nil {
		t.Fatal(err)
	}
	if p != (person{"Ada Example", 36}) || sa.count() != 1 || len(e.ledger()) != 1 {
		t.Fatalf("p = %+v calls %d ledger %d", p, sa.count(), len(e.ledger()))
	}
}

func TestLLM_Structured_Repaired(t *testing.T) {
	e, sa, sb := pair(t, seqHandler(`{"name":"Ada Example"}`, `{"name":"Ada Example","age":36}`), okHandler("x"))
	var p person
	resp, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &p)
	if err != nil || p.Age != 36 {
		t.Fatalf("p = %+v err = %v", p, err)
	}
	rows := e.ledger()
	if len(rows) != 2 || rows[0].Provider != "a" || rows[1].Provider != "a" || rows[1].Model != "m1" || sb.count() != 0 || sa.count() != 2 {
		t.Fatalf("ledger = %+v", rows)
	}
	if resp.Attempts != 2 {
		t.Fatalf("attempts = %d", resp.Attempts)
	}
	// The repair message carries the validator's text and the previous reply.
	raw, _ := json.Marshal(sa.last)
	if !strings.Contains(string(raw), `missing required property`) || !strings.Contains(string(raw), "Ada Example") {
		t.Fatalf("repair prompt = %s", raw)
	}
}

func TestLLM_Structured_StillInvalid(t *testing.T) {
	e, sa, sb := pair(t, seqHandler(`{"name":""}`, `not json at all`), okHandler(`{"name":"X","age":1}`))
	var p person
	_, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &p)
	wantKind(t, err, KindInvalidStructure)
	if sb.count() != 0 || sa.count() != 2 || len(e.ledger()) != 2 {
		t.Fatalf("fallback calls %d primary calls %d", sb.count(), sa.count())
	}
}

// A repair that the deadline cannot hold (less time left than the first reply
// took) is not started: the caller's budget is not spent on a cut-off call.
func TestLLM_Structured_NoRepairWhenTheDeadlineCannotHoldIt(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request, n int) {
		time.Sleep(900 * time.Millisecond)
		oaStream(w, "stop", 10, 5, 0, `{"name":"Ada Example"}`)
	}
	e, sa, _ := pair(t, slow, okHandler("x"))
	var p person
	_, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who", Deadline: time.Now().Add(1500 * time.Millisecond)}, json.RawMessage(personSchema), &p)
	wantKind(t, err, KindInvalidStructure)
	if !strings.Contains(err.Error(), "no time for a repair") || sa.count() != 1 {
		t.Fatalf("err = %v, calls = %d", err, sa.count())
	}
}

func TestLLM_Structured_RepairStaysOnTheCandidateThatAnswered(t *testing.T) {
	e, sa, sb := pair(t, statusHandler(503, `{}`), seqHandler(`{"name":"Ada"}`, `{"name":"Ada","age":2}`))
	var p person
	if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &p); err != nil {
		t.Fatal(err)
	}
	if sa.count() != 1 || sb.count() != 2 {
		t.Fatalf("primary %d fallback %d", sa.count(), sb.count())
	}
}

func TestLLM_Structured_PromptShapeWhenPresetLacksSchema(t *testing.T) {
	for _, supported := range []bool{true, false} {
		e := newEnv(t)
		s := newOAServer(t, seqHandler(`{"name":"Ada","age":2}`))
		p := oaProvider("a", s)
		p.StructuredOutput = supported
		e.provider(p, fakeKey("so"))
		e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
		var out person
		if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &out); err != nil {
			t.Fatal(err)
		}
		_, hasFormat := s.last["response_format"]
		raw, _ := json.Marshal(s.last["messages"])
		inPrompt := strings.Contains(string(raw), `required`)
		if hasFormat != supported || inPrompt == supported {
			t.Errorf("supported=%v: response_format=%v schema in prompt=%v", supported, hasFormat, inPrompt)
		}
	}
}

// OpenAI's models answer 400 to a response schema that is not in the strict
// form (seen live on deep-eval through OpenRouter): the sent copy closes
// every object, lists every property as required and makes the optional ones
// nullable, and the nulls standing for "not supplied" are gone before the
// reply is validated against the caller's own, looser schema.
func TestLLM_Structured_OpenAI_SendsTheStrictFormAndDropsOptionalNulls(t *testing.T) {
	const schema = `{"type":"object","required":["name"],"properties":{"name":{"type":"string","minLength":1},` +
		`"tags":{"type":"array","items":{"type":"string"}},"mode":{"type":"string","enum":["a","b"]},` +
		`"home":{"type":"object","required":["city"],"properties":{"city":{"type":"string"},"zip":{"type":"string"},"note":{"type":["string","null"]}}}}}`
	e := newEnv(t)
	s := newOAServer(t, seqHandler(`{"name":"Ada","tags":null,"mode":null,"home":{"city":"Paris","zip":null,"note":null}}`))
	p := oaProvider("a", s)
	p.StructuredOutput = true
	e.provider(p, fakeKey("so"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	var out struct {
		Name string
		Tags []string
		Home struct{ City, Zip string }
	}
	if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(schema), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "Ada" || out.Tags != nil || out.Home.City != "Paris" {
		t.Fatalf("out = %+v", out)
	}
	sent, _ := json.Marshal(s.last["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"])
	var got struct {
		Required             []string
		AdditionalProperties *bool
		Properties           map[string]struct {
			Type                 any
			Enum                 []any
			Required             []string
			AdditionalProperties *bool
			Properties           map[string]struct{ Type any }
		}
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatal(err)
	}
	home := got.Properties["home"]
	switch {
	case got.AdditionalProperties == nil || *got.AdditionalProperties || home.AdditionalProperties == nil || *home.AdditionalProperties:
		t.Errorf("an object is not closed: %s", sent)
	case strings.Join(got.Required, ",") != "home,mode,name,tags" || strings.Join(home.Required, ",") != "city,note,zip":
		t.Errorf("not every property is required: %s", sent)
	case fmt.Sprint(got.Properties["name"].Type) != "string" || fmt.Sprint(got.Properties["tags"].Type) != "[array null]":
		t.Errorf("only the optional properties are nullable: %s", sent)
	case fmt.Sprint(got.Properties["mode"].Enum) != "[a b <nil>]" || fmt.Sprint(home.Properties["note"].Type) != "[string null]":
		t.Errorf("an optional enum takes null, a nullable type is unchanged: %s", sent)
	}
}

func TestLLM_Structured_Anthropic_UsesTheSchemaFormat(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = decodeBody(r, &body)
		anStream(w, "end_turn", 10, 5, 0, `{"name":"Ada","age":2}`)
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, fakeKey("an"))
	e.route("narrative", Route{Provider: "an", Model: "claude-sonnet-4-6", Thinking: ThinkOff})
	var out person
	if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &out); err != nil {
		t.Fatal(err)
	}
	format, _ := body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil || out.Age != 2 {
		t.Fatalf("request = %v out = %+v", body, out)
	}
}

func TestLLM_Schema_UnsupportedKeywordRefusedUpFront(t *testing.T) {
	e, sa, _ := pair(t, okHandler("x"), okHandler("x"))
	var v any
	_, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "p"}, json.RawMessage(`{"$ref":"#/defs/x"}`), &v)
	wantKind(t, err, KindBadRequest)
	// A map is sent closed by every provider, so it is refused too.
	_, err = e.r.Structured(bg(), Request{Task: "narrative", Prompt: "p"}, json.RawMessage(`{"type":"object","properties":{"notes":{"type":"object","additionalProperties":{"type":"string"}}}}`), &v)
	wantKind(t, err, KindBadRequest)
	if sa.count() != 0 {
		t.Fatal("a request was made for an unusable schema")
	}
}

func TestLLM_Schema_Validator(t *testing.T) {
	cases := []struct {
		name, schema, value string
		ok                  bool
	}{
		{"type mismatch", `{"type":"string"}`, `3`, false},
		{"integer", `{"type":"integer"}`, `3.5`, false},
		{"enum", `{"enum":["a","b"]}`, `"c"`, false},
		{"enum ok", `{"enum":["a","b"]}`, `"b"`, true},
		{"nested array item", `{"type":"array","items":{"type":"number"},"maxItems":2}`, `[1,2,3]`, false},
		{"pattern", `{"type":"string","pattern":"^a+$"}`, `"ab"`, false},
		{"anyOf", `{"anyOf":[{"type":"string"},{"type":"null"}]}`, `null`, true},
		{"oneOf both", `{"oneOf":[{"type":"number"},{"type":"integer"}]}`, `1`, false},
		{"extra property", `{"type":"object","additionalProperties":false}`, `{"x":1}`, false},
		{"maximum", `{"type":"number","maximum":5}`, `6`, false},
	}
	for _, c := range cases {
		parsed, err := checkSchema(json.RawMessage(c.schema))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		_, problems := parseAndValidate(parsed, c.value)
		if (len(problems) == 0) != c.ok {
			t.Errorf("%s: problems = %v", c.name, problems)
		}
	}
}

func TestLLM_ExtractJSON(t *testing.T) {
	cases := map[string]string{
		"Sure! {\"a\": \"}\"} trailing": `{"a": "}"}`,
		"```json\n[1,2]\n```":           `[1,2]`,
		`{"a":{"b":[1]}} {"c":1}`:       `{"a":{"b":[1]}}`,
	}
	for in, want := range cases {
		got, err := extractJSON(in)
		if err != nil || string(got) != want {
			t.Errorf("extractJSON(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := extractJSON(`{"a":`); err == nil {
		t.Error("unterminated value accepted")
	}
}

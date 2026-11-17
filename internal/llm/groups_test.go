package llm

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
)

// groupEnv has providers a, b, c ... over servers with the given handlers, and
// a group g over the first members of them, a/ma, b/mb ... The narrative row
// names the group.
func groupEnv(t *testing.T, mode string, members int, fallbacks []Fallback, handlers ...func(http.ResponseWriter, *http.Request, int)) (*env, []*oaServer) {
	t.Helper()
	e := newEnv(t)
	var servers []*oaServer
	g := Group{Mode: mode}
	for i, h := range handlers {
		id := string(rune('a' + i))
		s := newOAServer(t, h)
		servers = append(servers, s)
		e.provider(oaProvider(id, s), fakeKey("g"+id))
		if i < members {
			g.Members = append(g.Members, Fallback{id, "m" + id})
		}
	}
	row := Route{Provider: GroupProvider, Model: "g", Thinking: ThinkOff, Fallbacks: fallbacks}
	if err := e.r.PatchRouting(bg(), map[string]*Route{"narrative": &row}, map[string]*Group{"g": &g}); err != nil {
		t.Fatal(err)
	}
	return e, servers
}

func (e *env) callNarrative() {
	e.t.Helper()
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		e.t.Fatal(err)
	}
}

func seedCalls(t *testing.T, e *env, provider, model string, n int) {
	t.Helper()
	err := e.st.Write(bg(), func(tx *sql.Tx) error {
		for range n {
			if _, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, billing)
				VALUES ('seed', ?, 'narrative', ?, 'openai-compatible', ?, 1, 'primary', 'ok', 'unpriced')`, timeText(e.clock.now()), provider, model); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLLM_Groups_Validation(t *testing.T) {
	s := Settings{Providers: []ProviderConfig{{ID: "a", Kind: KindFake}, {ID: "b", Kind: KindFake}},
		Groups: map[string]Group{"g": {Mode: GroupOrder, Members: []Fallback{{"a", "m1"}}}}}
	nine := Group{Mode: GroupOrder}
	for i := range 9 {
		nine.Members = append(nine.Members, Fallback{"a", "m" + string(rune('0'+i))})
	}
	ab := []Fallback{{"a", "m1"}, {"b", "m2"}}
	for _, tc := range []struct {
		name  string
		group string
		g     Group
		want  string // a fragment of the refusal; empty accepts
	}{
		{"ok", "fast-pool_2", Group{GroupRotate, ab}, ""},
		{"nine members", "g", nine, "1 to 8"},
		{"name too long", strings.Repeat("a", 41), Group{GroupOrder, ab}, "name"},
		{"slash in name", "a/b", Group{GroupOrder, ab}, "name"},
		{"unknown mode", "g", Group{"random", ab}, "mode"},
		{"no members", "g", Group{GroupOrder, nil}, "members: 1 to 8"},
		{"nested group", "g", Group{GroupOrder, []Fallback{{"a", "m1"}, {GroupProvider, "other"}}}, "cannot hold a group"},
		{"pi login", "g", Group{GroupOrder, []Fallback{{"pi:openai-codex", "m"}}}, "agent runtime"},
		{"unconfigured provider", "g", Group{GroupOrder, []Fallback{{"zzz", "m1"}}}, "not configured"},
		{"member twice", "g", Group{GroupOrder, []Fallback{{"a", "m1"}, {"a", "m1"}}}, "twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.ValidateGroup(tc.group, tc.g)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("ValidateGroup = %v, want %q", err, tc.want)
			}
		})
	}
	// Eight members are the most.
	eight := Group{GroupOrder, nine.Members[:8]}
	if err := s.ValidateGroup("g", eight); err != nil {
		t.Fatalf("eight members: %v", err)
	}

	for _, tc := range []struct {
		name string
		task string
		row  Route
		want string
	}{
		{"defined group", "narrative", Route{Provider: GroupProvider, Model: "g"}, ""},
		{"undefined group", "narrative", Route{Provider: GroupProvider, Model: "nope"}, "not defined"},
		{"group as fallback", "narrative", Route{Provider: "a", Model: "m1", Fallbacks: []Fallback{{GroupProvider, "g"}}}, "primary, not a fallback"},
		{"embedding task", "embedding", Route{Provider: GroupProvider, Model: "g"}, "embedding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.ValidateRoute(tc.task, tc.row)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("ValidateRoute = %v, want %q", err, tc.want)
			}
		})
	}
	if err := (ProviderConfig{ID: GroupProvider, Kind: KindFake}).Validate(); err == nil {
		t.Fatal("a provider may not take the id the rows name groups by")
	}
}

func TestLLM_Groups_DeleteIsRefusedWhileRowsNameTheGroup(t *testing.T) {
	e, _ := groupEnv(t, GroupOrder, 2, nil, okHandler("a"), okHandler("b"))
	e.route("quick-screen", Route{Provider: GroupProvider, Model: "g"})
	err := e.r.PatchRouting(bg(), nil, map[string]*Group{"g": nil})
	if err == nil || !strings.Contains(err.Error(), "routes.narrative") || !strings.Contains(err.Error(), "routes.quick-screen") {
		t.Fatalf("delete = %v; want both rows named", err)
	}
	if _, ok := e.load().Groups["g"]; !ok || e.resolve("narrative").Provider != GroupProvider {
		t.Fatal("a refused delete changed the settings")
	}
	// The same patch may move the rows off the group first.
	off := Route{Provider: "a", Model: "m1"}
	err = e.r.PatchRouting(bg(), map[string]*Route{"narrative": &off, "quick-screen": nil}, map[string]*Group{"g": nil})
	if err != nil || len(e.load().Groups) != 0 {
		t.Fatalf("delete after moving the rows: %v, groups %v", err, e.load().Groups)
	}
}

func TestLLM_Groups_ModeDecidesTheFirstMember(t *testing.T) {
	ok := okHandler("ok")
	t.Run("order keeps the list", func(t *testing.T) {
		e, _ := groupEnv(t, GroupOrder, 3, nil, ok, ok, ok)
		for range 3 {
			e.callNarrative()
		}
		if got := e.attempts(); got != "a::,a::,a::" {
			t.Fatalf("attempts = %s", got)
		}
	})
	t.Run("rotate starts at the next member each call", func(t *testing.T) {
		e, _ := groupEnv(t, GroupRotate, 3, nil, ok, ok, ok)
		for range 4 {
			e.callNarrative()
		}
		// The reason is set only where the mode moved the head off the first listed.
		if got := e.attempts(); got != "a::,b:rotate:,c:rotate:,a::" {
			t.Fatalf("attempts = %s", got)
		}
	})
	t.Run("usage puts the least used first and spreads a burst", func(t *testing.T) {
		e, _ := groupEnv(t, GroupUsage, 3, nil, ok, ok, ok)
		seedCalls(t, e, "a", "ma", 3)
		seedCalls(t, e, "b", "mb", 1)
		seedCalls(t, e, "c", "mc", 1)
		const seeded = "a::,a::,a::,b::,c::," // the ledger rows above, which attempts lists first
		for range 3 {
			e.callNarrative()
		}
		// b and c tie at one and b is listed first; each pick counts at once, so c follows, then b.
		if got := strings.TrimPrefix(e.attempts(), seeded); got != "b:usage:,c:usage:,b:usage:" {
			t.Fatalf("attempts = %s", got)
		}
		// A new read of the ledger (a, b, c at 3, 3, 2 now) replaces the counts.
		e.clock.advance(capRefresh + time.Second)
		e.callNarrative()
		if got := e.attempts(); !strings.HasSuffix(got, ",c:usage:") {
			t.Fatalf("attempts = %s", got)
		}
	})
	t.Run("usage with no calls today is the list order", func(t *testing.T) {
		e, _ := groupEnv(t, GroupUsage, 2, nil, ok, ok)
		e.callNarrative()
		if got := e.attempts(); got != "a::" {
			t.Fatalf("attempts = %s", got)
		}
	})
}

func TestLLM_Groups_FallbacksFollowTheMembersAndRestsFollowTheMode(t *testing.T) {
	down := statusHandler(503, `{}`)
	t.Run("the row's fallbacks come after every member", func(t *testing.T) {
		e, servers := groupEnv(t, GroupRotate, 2, []Fallback{{"c", "mc"}}, down, down, okHandler("from c"))
		e.callNarrative()
		e.clock.advance(time.Hour) // the rests are over; the rotation starts at b
		e.callNarrative()
		if got := e.attempts(); got != "a::,b:fallback:,c:fallback:,b:rotate:,a:fallback:,c:fallback:" {
			t.Fatalf("attempts = %s", got)
		}
		if servers[0].count() != 2 || servers[1].count() != 2 || servers[2].count() != 2 {
			t.Fatalf("calls a %d, b %d, c %d", servers[0].count(), servers[1].count(), servers[2].count())
		}
	})
	t.Run("a resting member goes behind whatever the mode chose", func(t *testing.T) {
		failFirst := func(w http.ResponseWriter, r *http.Request, n int) {
			if n == 1 {
				oaFail(w, 503, `{}`)
				return
			}
			oaStream(w, "stop", 10, 5, 0, "ok")
		}
		e, _ := groupEnv(t, GroupRotate, 2, nil, failFirst, okHandler("ok"))
		for range 3 { // starts at a (fails, rests), at b, and at a again while a rests
			e.callNarrative()
		}
		if got := e.attempts(); got != "a::,b:fallback:,b:rotate:,b:rested:" {
			t.Fatalf("attempts = %s", got)
		}
	})
}

func TestLLM_Groups_TheCapDowngradeSeesTheMembers(t *testing.T) {
	e, models := capEnv(t)
	g := Group{Mode: GroupOrder, Members: []Fallback{{"a", "gpt-6.1-sol"}, {"a", "gpt-6-luna"}}}
	row := Route{Provider: GroupProvider, Model: "g", Thinking: ThinkOff}
	if err := e.r.PatchRouting(bg(), map[string]*Route{"narrative": &row}, map[string]*Group{"g": &g}); err != nil {
		t.Fatal(err)
	}
	e.callNarrative()
	if models.lastModel() != "gpt-6.1-sol" {
		t.Fatalf("under the cap the first member serves, got %s", models.lastModel())
	}
	addSpend(t, e, 2.05)
	e.clock.advance(capRefresh + time.Second)
	e.callNarrative()
	if models.lastModel() != "gpt-6-luna" || !strings.HasSuffix(e.reasons(), ",cap-downgrade") {
		t.Fatalf("over the cap the cheaper member serves: %s, reasons %s", models.lastModel(), e.reasons())
	}
}

func TestLLM_Groups_AgentGatewayKeepsAConversationOnItsMember(t *testing.T) {
	stream := func(text string) func(http.ResponseWriter, *http.Request, int) {
		return func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "stop", 3000, 5, 2900, text) }
	}
	e, _, _ := gwEnv(t, "deepseek-flash", stream("from a"), stream("from b"))
	g := Group{Mode: GroupRotate, Members: []Fallback{{"a", "deepseek-flash"}, {"b", "openai/gpt-6-luna"}}}
	row := Route{Provider: GroupProvider, Model: "g", Thinking: ThinkMedium, MaxOutputTokens: 16000}
	if err := e.r.PatchRouting(bg(), map[string]*Route{AgentTask: &row}, map[string]*Group{"g": &g}); err != nil {
		t.Fatal(err)
	}
	if route, err := e.r.AgentRoute(bg(), AgentTask); err != nil || route.Model != "deepseek-flash" || route.Config.ID != "a" {
		t.Fatalf("pi is launched with the first member: %+v, %v", route, err)
	}
	for _, c := range [][2]string{{"ses_1", "t1"}, {"ses_1", "t1"}, {"ses_2", "t1"}, {"ses_3", "t1"}} {
		if _, err := e.agentCall(c[0], c[1], piRequest()); err != nil {
			t.Fatal(err)
		}
	}
	// The second call of ses_1 stays on a (affinity) and uses up no turn of the
	// rotation (magpie rotates per round, not per call), so ses_2 takes b and ses_3 a.
	if got := e.attempts(); got != "a::ses_1,a:affinity:ses_1,b:rotate:ses_2,a::ses_3" {
		t.Fatalf("attempts = %s", got)
	}
}

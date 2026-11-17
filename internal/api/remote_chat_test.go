package api

import (
	"bufio"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
)

// catalogTurn reports the tools the turn is offered, then answers.
func catalogTurn(catalogs chan<- []string) func(*scriptedPi) {
	return func(p *scriptedPi) {
		_, cat := p.bridge("GET", "/internal/agent/catalog", nil)
		tools, ok := cat["tools"].([]any)
		if !ok {
			// A process launched as its test ends finds the server gone; the
			// test that needed this turn fails on its own wait instead.
			return
		}
		var names []string
		for _, t := range tools {
			names = append(names, t.(map[string]any)["name"].(string))
		}
		catalogs <- names
		p.bridge("POST", "/internal/agent/hello", map[string]any{"tools": names})
		<-p.prompt
		p.bridge("GET", "/internal/agent/turn-context", nil)
		p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "That needs the desktop."}})
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "That needs the desktop."}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	}
}

func TestRemote_ChatOffersReadAndDraftToolsOnly(t *testing.T) {
	catalogs := make(chan []string, 4)
	rig := &remoteRig{}
	h, l := chatHarness(t, catalogTurn(catalogs), rig.mod())
	rig.harness = h
	rig.reserve()
	rig.online()
	l.release()

	// The dashboard session's turn is the baseline: it has every class.
	cookie := h.session()
	if r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "Help me think through my week."}); r.StatusCode != 202 {
		t.Fatalf("dashboard send: %d %s", r.StatusCode, r.body)
	}
	local := <-catalogs

	dev := rig.pair("Phone")
	sent := rig.device(dev, "POST", "/api/chat/sessions/new/messages", map[string]string{"text": "start an apply run for me"})
	if sent.status != 202 {
		t.Fatalf("remote send: %d %s", sent.status, sent.body)
	}
	remote := <-catalogs
	if len(remote) == 0 || len(remote) >= len(local) {
		t.Fatalf("the remote turn has %d tools, the dashboard turn %d", len(remote), len(local))
	}
	for _, name := range remote {
		if class := agent.ToolClass(name); class != "read" && class != "draft" {
			t.Errorf("the remote turn is offered %s (class %s)", name, class)
		}
	}
	for _, name := range []string{"autofill_application", "apply_from_list", "scan_portal", "evaluate_job"} {
		if !slices.Contains(local, name) {
			continue // not built in this stack; the unit tests cover the classes
		}
		if slices.Contains(remote, name) {
			t.Errorf("the remote turn is offered %s", name)
		}
	}
	if !slices.ContainsFunc(local, func(n string) bool { c := agent.ToolClass(n); return c == "write-local" || c == "external-action" }) {
		t.Fatal("the dashboard turn has no write or action tool, so the comparison proves nothing")
	}

	// The device reads the turn's stream through the tunnel and sees it end.
	// The turn can end before the stream opens, so it reads from the first
	// event of this run instead of racing it.
	sid := sent.data()["sessionId"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := rig.request(ctx, tcall{path: "/api/chat/sessions/" + sid + "/events", cookie: dev.token, headers: map[string]string{"Last-Event-ID": h.srv.epoch + "-0"}})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("remote stream: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	final := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"type":"`+wire.ChatFinal+`"`) {
			final = true
			break
		}
	}
	if !final {
		t.Error("the remote stream never reached the final event")
	}
}

func TestRemote_ChatSendRefusesTheLocalToken(t *testing.T) {
	r := newRemoteRig(t)
	if got := r.send("POST", "/api/chat/sessions/new/messages", map[string]string{"text": "hello"}); got.StatusCode != 403 || got.errReason() != wire.ReasonSessionOnly {
		t.Errorf("local token: %d %s", got.StatusCode, got.body)
	}
}

// A remote device posts to and reads only the conversations it started; the
// desktop's conversations, and another device's, are not visible to it.
func TestRemote_ChatSessionsBelongToTheDeviceThatStartedThem(t *testing.T) {
	catalogs := make(chan []string, 8)
	rig := &remoteRig{}
	h, l := chatHarness(t, catalogTurn(catalogs), rig.mod())
	rig.harness = h
	rig.reserve()
	rig.online()
	l.release()

	cookie := h.session()
	local := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "Help me think through my week."})
	if local.StatusCode != 202 {
		t.Fatalf("local send: %d %s", local.StatusCode, local.body)
	}
	localID := local.data()["sessionId"].(string)
	<-catalogs

	alex := rig.pair("Alex's phone")
	sam := rig.pair("Sam's tablet")
	mine := rig.device(alex, "POST", "/api/chat/sessions/new/messages", map[string]string{"text": "Summarise my day."})
	if mine.status != 202 {
		t.Fatalf("remote send: %d %s", mine.status, mine.body)
	}
	alexID := mine.data()["sessionId"].(string)
	<-catalogs

	read := func(d paired, id string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := rig.request(ctx, tcall{path: "/api/chat/sessions/" + id + "/events", cookie: d.token})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := read(alex, alexID); got != 200 {
		t.Errorf("the device reading its own conversation: %d", got)
	}
	for name, id := range map[string]string{"the desktop's conversation": localID, "another device's": alexID} {
		who := alex
		if id == alexID {
			who = sam
		}
		if got := read(who, id); got != 404 {
			t.Errorf("reading %s: %d", name, got)
		}
		post := rig.device(who, "POST", "/api/chat/sessions/"+id+"/messages", map[string]string{"text": "hello"})
		if post.status != 404 || post.errCode() != "not_found" {
			t.Errorf("posting to %s: %d %s", name, post.status, post.body)
		}
	}
	// It may continue its own.
	deadline := time.Now().Add(10 * time.Second)
	for {
		again := rig.device(alex, "POST", "/api/chat/sessions/"+alexID+"/messages", map[string]string{"text": "And tomorrow?"})
		if again.status == 202 {
			break
		}
		if again.status != 409 || time.Now().After(deadline) { // the first turn may still be running
			t.Fatalf("continuing its own conversation: %d %s", again.status, again.body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The dashboard still sees every conversation.
	if got := h.get("/api/chat/sessions/" + alexID); got.StatusCode != 200 {
		t.Errorf("the desktop reading the device's conversation: %d", got.StatusCode)
	}
}

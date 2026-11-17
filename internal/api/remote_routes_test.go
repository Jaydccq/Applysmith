package api

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/api/wire"
)

func goldenRoutes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/remote-routes.golden")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return out
}

// Any widening of the remote surface appears as a change to the golden file.
func goldenMatchesTheRouteTable(t *testing.T) {
	var table []string
	for _, r := range Routes() {
		if r.Remote == RemoteAllowed {
			table = append(table, r.Pattern())
		}
	}
	slices.Sort(table)
	golden := goldenRoutes(t)
	for _, p := range table {
		if !slices.Contains(golden, p) {
			t.Errorf("%s is remote: allowed in the route table but missing from testdata/remote-routes.golden", p)
		}
	}
	for _, p := range golden {
		if !slices.Contains(table, p) {
			t.Errorf("%s is in testdata/remote-routes.golden but not remote: allowed in the route table", p)
		}
	}
}

// The remote allowlist is the list the specification gives, and nothing under
// /api/browser/ is on it but the lane screenshot route.
func allowlistIsTheSpecifiedSet(t *testing.T) {
	want := []string{
		"GET /{path...}", "GET /pair", "GET /api/health",
		"GET /api/board/events", "GET /api/candidacies", "GET /api/candidacies/{id}", "GET /api/applications/lookup",
		"GET /api/reports", "GET /api/reports/{number}",
		"GET /api/runs", "GET /api/runs/summary", "GET /api/runs/{id}", "GET /api/runs/{id}/events",
		"GET /api/needs-you", "GET /api/apply/list", "GET /api/schedules", "GET /api/notifications/events",
		"POST /api/runs/{id}/cancel", "GET /api/browser/lanes/{lane}/tabs/{tabId}/screenshot",
		"POST /api/notifications/{announcementId}/ack", "POST /api/runs/{id}/ack", "POST /api/runs/{id}/seen",
		"POST /api/chat/sessions/{id}/messages", "GET /api/chat/sessions/{id}/events",
		"GET /api/remote/me",
		// Needed by the above: the claim the pairing page posts, and the two
		// approval routes the local-api specification marks remote-allowed.
		"POST /api/remote/pair/claim", "POST /api/agent/confirmations/{id}/approve", "DELETE /api/agent/approvals/{id}",
	}
	slices.Sort(want)
	if got := goldenRoutes(t); !slices.Equal(got, want) {
		t.Errorf("the golden file differs from the specified set:\n got %v\nwant %v", got, want)
	}
	for _, r := range Routes() {
		if r.Remote == RemoteAllowed && strings.HasPrefix(r.Path, "/api/browser/") && !strings.HasSuffix(r.Path, "/screenshot") {
			t.Errorf("%s: only the lane screenshot route under /api/browser/ may be remote-allowed", r.Pattern())
		}
		if r.Remote == RemoteAllowed && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH") && r.Body != BodyNone && r.Body != BodyJSON {
			t.Errorf("%s: a remote route takes a json body or none, not %s", r.Pattern(), r.Body)
		}
	}
}

// A route with the secret flag, the session-only flag, the admin class or the
// process class is never remote-allowed, and the table refuses to build with one.
func neverAllowedForAdminSecretSessionOnlyOrProcessRoutes(t *testing.T) {
	for _, r := range Routes() {
		if r.Remote == RemoteAllowed && (r.Access == AccessAdmin || r.Access == AccessAgentProcess || r.Secret || r.SessionOnly) {
			t.Errorf("%s is remote: allowed", r.Pattern())
		}
	}
	for name, mod := range map[string]func(*Route){
		"admin":        func(r *Route) { r.Access = AccessAdmin },
		"secret":       func(r *Route) { r.Secret = true },
		"session-only": func(r *Route) { r.SessionOnly = true },
		"process":      func(r *Route) { r.Access = AccessAgentProcess },
	} {
		rt := route("GET", "/api/x", reads("Health"))
		rt.Remote = RemoteAllowed
		mod(&rt)
		if rt.Access == AccessAgentProcess {
			rt.Path = "/internal/agent/x"
		}
		if problems := validateRoutes([]Route{rt}); len(problems) == 0 {
			t.Errorf("%s: the table accepted a remote-allowed route", name)
		}
	}
}

func TestRemote_RouteTableGoldenFile_NoSecretOrAdminRouteRemote(t *testing.T) {
	t.Run("the table and the golden file agree", goldenMatchesTheRouteTable)
	t.Run("the golden file is the specified set", allowlistIsTheSpecifiedSet)
	t.Run("no admin, secret, session-only or process route is remote", neverAllowedForAdminSecretSessionOnlyOrProcessRoutes)
}

// Every route class meets every kind of caller on the tunnel listener, and the
// device token meets the loopback listener: the answers are fixed by the
// table, so a remote token can never reach an admin or secret route.
func TestRemoteRouteClassPrincipalMatrix(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	// The screenshot route is remote-allowed only while the policy switch is on.
	r.setPolicy(set("remote_access.allow_screenshots", "true"))
	r.srv.remote.reconcile()
	dev := r.pair("Phone")
	session := r.session()

	body := func(rt Route) (string, []byte) {
		switch rt.Body {
		case BodyJSON, BodyJSONLarge:
			return "application/json", []byte("{}")
		case BodyUpload:
			return "application/octet-stream", []byte("x")
		}
		return "", nil
	}
	forbidden := func(res tres) bool { return res.status == 403 && res.errReason() == wire.ReasonRemoteForbidden }

	var denied, allowed int
	for _, rt := range routeDefs {
		path := sampleValues.Replace(rt.Path)
		ct, b := body(rt)
		isAllowed := rt.Remote == RemoteAllowed
		if isAllowed {
			allowed++
		} else {
			denied++
		}
		name := rt.Pattern()
		step := func() { r.clock.advance(2 * time.Second) } // keeps the per-credential limit out of the way

		// No credential.
		none := r.tdo(tcall{method: rt.Method, path: path, contentType: ct, body: b})
		step()
		switch {
		case !isAllowed && !forbidden(none):
			t.Errorf("%s without a credential: %d %s", name, none.status, none.body)
		case isAllowed && rt.Access != AccessPublic && none.status != 401:
			t.Errorf("%s without a credential: %d %s", name, none.status, none.body)
		case isAllowed && rt.Access == AccessPublic && rt.Path != "/{path...}" && (none.status == 401 || forbidden(none)): // the catch-all answers an asset without a credential 401 by design
			t.Errorf("%s is public and remote-allowed but answered %d %s", name, none.status, none.body)
		}

		// A paired device.
		c := asDevice(dev, rt.Method, path)
		c.contentType, c.body = ct, b
		got := r.tdo(c)
		step()
		switch {
		case !isAllowed && !forbidden(got):
			t.Errorf("%s with a device token: %d %s", name, got.status, got.body)
		case isAllowed && (got.status == 401 || forbidden(got)):
			t.Errorf("%s is remote-allowed but a device was refused: %d %s", name, got.status, got.body)
		}

		// The local bearer token and a dashboard session, presented to the tunnel.
		for who, c := range map[string]tcall{
			"local token":       {method: rt.Method, path: path, bearer: r.token(), contentType: ct, body: b},
			"dashboard session": {method: rt.Method, path: path, session: session, origin: "https://" + tunnelHost, contentType: ct, body: b},
		} {
			res := r.tdo(c)
			step()
			switch {
			case !isAllowed && !forbidden(res):
				t.Errorf("%s with a %s on the tunnel: %d %s", name, who, res.status, res.body)
			case isAllowed && rt.Access != AccessPublic && res.status != 401:
				t.Errorf("%s with a %s on the tunnel: %d %s", name, who, res.status, res.body)
			}
		}

		// The device token on the primary listener.
		if rt.Access != AccessPublic {
			res := r.do(call{method: rt.Method, path: path, bearer: dev.token, contentType: ct, body: b})
			step()
			if res.StatusCode != 401 || res.errCode() != "unauthenticated" {
				t.Errorf("%s with a device token on the loopback listener: %d %s", name, res.StatusCode, res.body)
			}
		}
	}
	if denied < 200 || allowed < 25 {
		t.Errorf("the matrix covered %d denied and %d allowed routes; the table changed shape", denied, allowed)
	}
}

func TestRemote_HeldSubmitNotConfirmableByARemoteDevice(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	d, _ := r.srv.dom()

	submit := heldCardFor(t, r.harness, "autofill_application", `{"url":"https://jobs.example.test/a/1","submit":true}`, 4, agent.ChannelDashboard, "apply to that job")
	got := r.device(dev, "POST", "/api/agent/confirmations/"+submit+"/approve", nil)
	if got.status != 403 {
		t.Fatalf("a device approved a held submit: %d %s", got.status, got.body)
	}
	if card, _ := approvals.New(r.srv.store.Load(), nil, nil, nil).Get(context.Background(), submit); card.State != approvals.Pending {
		t.Errorf("the refused approval left the card %s; the run must stay held", card.State)
	}
	if got := r.device(dev, "POST", "/api/agent/confirmations/"+submit+"/approve-standing", map[string]string{"scope": "today"}); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
		t.Errorf("approve-standing from a device: %d %s", got.status, got.body)
	}

	// One-time approval of an ordinary held call is what a device may do.
	fact := heldCard(t, r.harness, "save_fact", `{"body":"approved from a phone"}`, 2, agent.ChannelDashboard)
	if got := r.device(dev, "POST", "/api/agent/confirmations/"+fact+"/approve", nil); got.status != 200 {
		t.Errorf("one-time approval from a device: %d %s", got.status, got.body)
	}
	if act, _ := d.confirmations.Active(context.Background()); len(act) != 0 {
		t.Errorf("a device created a standing approval: %+v", act)
	}
}

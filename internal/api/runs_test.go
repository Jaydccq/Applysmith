package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/identity"
	"applysmith/internal/store"
)

// The idempotent-run primitive of the store is proven through the route below,
// which only a test server has; it reads the key and the posting with the
// product's apikit.IdempotencyKey and identity.CanonicalURL. Evaluations and scans prove the same primitive
// through their production routes; the application start routes create no row
// of the runs table, so they cannot stand in for it.

type testRunRequest struct {
	PostingURL string `json:"postingUrl"`
}

func init() { wire.Types["TestRunRequest"] = reflect.TypeOf(testRunRequest{}) }

func (h *harness) runRoutes() []boundRoute {
	start := testRoute("POST", "/api/test/runs", func(r *Route) {
		r.Body, r.Request, r.Response = BodyJSON, "TestRunRequest", "RunAccepted"
	}, func(c *apikit.Call) (apikit.Reply, error) {
		key, e := apikit.IdempotencyKey(c)
		if e != nil {
			return apikit.Reply{}, e
		}
		var req testRunRequest
		if e := apikit.Decode(c.Body, &req); e != nil {
			return apikit.Reply{}, e
		}
		posting, err := identity.CanonicalURL(req.PostingURL)
		if err != nil {
			e := apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
			e.Details = &wire.ErrorDetails{Field: "postingUrl"}
			return apikit.Reply{}, e
		}
		input, err := json.Marshal(testRunRequest{PostingURL: posting})
		if err != nil {
			return apikit.Reply{}, err
		}
		st, err := h.srv.db()
		if err != nil {
			return apikit.Reply{}, err
		}
		ref, err := st.CreateRun(c.R.Context(), store.RunRequest{
			Principal: c.P.ID, Operation: "apply.start", Key: key, Kind: "apply", Input: input,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
			}
			return apikit.Reply{}, err
		}
		return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
	})
	return []boundRoute{start}
}

func applyBody(url string) []byte { return jsonBody(map[string]string{"postingUrl": url}) }

const postingA = "https://jobs.example.com/openings/1234"

func (h *harness) startApply(key, url string, extra ...call) result {
	h.t.Helper()
	c := call{method: "POST", path: "/api/test/runs", useToken: true, contentType: "application/json", body: applyBody(url),
		headers: map[string]string{"Idempotency-Key": key}}
	return h.do(c)
}

func TestIdempotencyKeyIsScopedToThePrincipal(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	viaToken := h.startApply("key-0000-0001", postingA)
	viaSession := h.do(call{method: "POST", path: "/api/test/runs", cookie: cookie, contentType: "application/json", body: applyBody(postingA),
		headers: map[string]string{"Idempotency-Key": "key-0000-0001", "Origin": h.srv.Origin()}})
	if viaSession.StatusCode != 202 || viaSession.data()["runId"] == viaToken.data()["runId"] || h.runCount() != 2 {
		t.Fatalf("session: %d %s", viaSession.StatusCode, viaSession.body)
	}
	// The dashboard keeps its scope across a restart, because its identifier is stable.
	h.restart()
	cookie = h.session()
	again := h.do(call{method: "POST", path: "/api/test/runs", cookie: cookie, contentType: "application/json", body: applyBody(postingA),
		headers: map[string]string{"Idempotency-Key": "key-0000-0001", "Origin": h.srv.Origin()}})
	if again.data()["runId"] != viaSession.data()["runId"] || h.runCount() != 2 {
		t.Fatalf("dashboard retry after restart: %s", again.body)
	}
}

func TestRunCreationRequiresAValidKey(t *testing.T) {
	h := newHarness(t)
	for name, c := range map[string]call{
		"no key":        {headers: map[string]string{}},
		"bad key chars": {headers: map[string]string{"Idempotency-Key": "key with spaces"}},
	} {
		c.method, c.path, c.useToken, c.contentType, c.body = "POST", "/api/test/runs", true, "application/json", applyBody(postingA)
		if r := h.do(c); r.StatusCode != 400 || r.errCode() != "bad_request" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if h.runCount() != 0 {
		t.Errorf("%d runs created by invalid requests", h.runCount())
	}
}

func TestUnknownRunIsNotFound(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/api/runs/run_missing", useToken: true})
	if r.StatusCode != 404 || r.errCode() != string(wire.CodeNotFound) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func strictInto(t *testing.T, body []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
}

// What the server writes must decode with the wire types, which are the
// definitions the generated TypeScript declarations come from.
func TestServerOutputDecodesWithTheWireTypes(t *testing.T) {
	h := newHarness(t)
	var health wire.Success[wire.Health]
	strictInto(t, h.do(call{path: "/api/health", useToken: true}).body, &health)
	if !health.OK || health.Data.Status != wire.HealthOK || len(health.Data.Components) != 7 || health.Data.StartedAt.IsZero() {
		t.Errorf("health %+v", health)
	}
	var fail wire.Failure
	strictInto(t, h.do(call{path: "/api/runs/none"}).body, &fail)
	if fail.OK || fail.Error.Code != wire.CodeUnauthenticated {
		t.Errorf("failure %+v", fail)
	}
	var limited wire.Failure
	for range 5 {
		h.claim("WRONGCODE0")
	}
	strictInto(t, h.claim("WRONGCODE0").body, &limited)
	if limited.Error.Code != wire.CodeRateLimited {
		t.Errorf("rate limited %+v", limited)
	}
	var accepted wire.Success[wire.RunAccepted]
	strictInto(t, h.startApply("key-0000-0001", postingA).body, &accepted)
	var view wire.Success[wire.RunView]
	strictInto(t, h.do(call{path: "/api/runs/" + accepted.Data.RunID, useToken: true}).body, &view)
	if view.Data.Status != "queued" || view.Data.Kind != "apply" {
		t.Errorf("view %+v", view)
	}
}

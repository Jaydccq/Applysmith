//go:build browser

package extension

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubAPI answers the routes the extension calls, with the same checks the
// real server makes: a bearer token, an issued run id, lowercase event ids.
type stubAPI struct {
	srv   *httptest.Server
	token string
	code  string
	stamp string

	mu       sync.Mutex
	runs     map[string]bool
	claims   int
	presses  []map[string]any
	subs     []map[string]any
	reads    []string
	matches  []map[string]any
	writes   []map[string]any
	events   []map[string]any
	pushes   []map[string]any
	ends     []string
	rejected []string
	raw      []string

	wrong     int
	lockUntil time.Time
	gate      chan struct{}
	held      int
	// Reads of the start can be held too, to keep a fill in its start: by path.
	startGates map[string]chan struct{}
	startHeld  map[string]int
}

var (
	idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	endPath        = regexp.MustCompile(`^/api/autofill/runs/(run_[0-9a-f]+)/end$`)
)

var eventID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func newStub(t *testing.T, stamp string) *stubAPI {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	s := &stubAPI{token: base64.RawURLEncoding.EncodeToString(raw[:]), code: "ABCDEFGHJK", stamp: stamp, runs: map[string]bool{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ok(w http.ResponseWriter, status int, data any) {
	reply(w, status, map[string]any{"ok": true, "requestId": "req_stub0001", "data": data})
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	reply(w, status, map[string]any{"ok": false, "requestId": "req_stub0001", "error": map[string]any{"code": code, "message": msg, "retryable": false}})
}

// lockSeconds is how long the stub's pairing throttle holds after too many wrong codes.
const lockSeconds = 2

// maxWrongCodes is a short stand-in for the real pairing throttle (internal/api
// limits: 5 claims of any outcome per key in a sliding 60 s window): this many
// wrong codes in a row pass, the next waits lockSeconds, and a right code resets
// the count, so the popup's throttled path is reached without a minute's wait.
const maxWrongCodes = 5

func throttled(w http.ResponseWriter, seconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	reply(w, 429, map[string]any{"ok": false, "requestId": "req_stub0001", "error": map[string]any{
		"code": "rate_limited", "message": "too many requests", "retryable": true, "details": map[string]any{"retryAfterSeconds": seconds},
	}})
}

// rotate replaces the token and the pairing code, as applysmith token rotate does.
func (s *stubAPI) rotate() {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = base64.RawURLEncoding.EncodeToString(raw[:])
	s.code = "QRSTUVWXYZ"
}

func (s *stubAPI) currentToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// hold makes the matcher wait until release is called; held requests end with the client.
func (s *stubAPI) hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = make(chan struct{})
}

func (s *stubAPI) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil {
		close(s.gate)
		s.gate = nil
	}
}

// heldCount is how many matcher requests have arrived while the gate was shut.
func (s *stubAPI) heldCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// holdStart makes the request to path wait until releaseStart is called.
func (s *stubAPI) holdStart(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startGates == nil {
		s.startGates, s.startHeld = map[string]chan struct{}{}, map[string]int{}
	}
	s.startGates[path] = make(chan struct{})
}

func (s *stubAPI) releaseStart(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.startGates[path]; g != nil {
		close(g)
		delete(s.startGates, path)
	}
}

func (s *stubAPI) startHeldCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startHeld[path]
}

func (s *stubAPI) runsIssued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *stubAPI) releaseAll() {
	s.release()
	for _, p := range []string{"/api/autofill/applicant", "/api/autofill/runs"} {
		s.releaseStart(p)
	}
}

func (s *stubAPI) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/answer-bank/match" || path == "/api/autofill/applicant" || path == "/api/autofill/runs" {
		s.mu.Lock()
		gate := s.gate
		if path != "/api/answer-bank/match" {
			gate = s.startGates[path]
			if gate != nil {
				s.startHeld[path]++
			}
		} else if gate != nil {
			s.held++
		}
		s.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
	}
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		s.mu.Lock()
		s.raw = append(s.raw, string(b))
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch path {
	case "/api/health":
		ok(w, 200, map[string]any{"status": "ok", "apiVersion": 1})
		return
	case "/api/pairing/claim":
		s.claims++
		if time.Now().Before(s.lockUntil) {
			throttled(w, lockSeconds)
			return
		}
		if s.wrong >= maxWrongCodes {
			s.wrong = 0
			s.lockUntil = time.Now().Add(lockSeconds * time.Second)
			throttled(w, lockSeconds)
			return
		}
		if body["code"] == s.code {
			s.code = ""
			s.wrong = 0
			ok(w, 200, map[string]any{"token": s.token})
			return
		}
		s.wrong++
		fail(w, 401, "unauthenticated", "the code is wrong, expired or used")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		s.rejected = append(s.rejected, path)
		fail(w, 401, "unauthenticated", "missing or wrong token")
		return
	}
	s.reads = append(s.reads, r.Method+" "+path)
	switch path {
	case "/api/answer-bank/settings":
		ok(w, 200, map[string]any{"llmFallbackEnabled": false, "groundingMode": "strict", "autoAcceptLegalDeclarations": false})
	case "/api/extension/build":
		ok(w, 200, map[string]any{"stamp": s.stamp, "version": "0.1.0"})
	case "/api/resumes":
		ok(w, 200, map[string]any{"resumes": []any{}})
	case "/api/autofill/applicant":
		ok(w, 200, map[string]any{
			"candidate": map[string]any{"fullName": "Alex Example", "firstName": "Alex", "lastName": "Example", "email": "alex.example@example.com", "address": map[string]any{}},
			"links":     map[string]any{}, "history": map[string]any{"employment": []any{}, "education": []any{}}, "askMe": []string{},
		})
	case "/api/autofill/runs":
		if !idempotencyKey.MatchString(r.Header.Get("Idempotency-Key")) {
			fail(w, 400, "bad_request", "an Idempotency-Key header is required")
			return
		}
		if body["host"] == nil || body["url"] == nil || len(body) != 2 {
			fail(w, 422, "validation_failed", "host and url are required")
			return
		}
		var id [12]byte
		_, _ = rand.Read(id[:])
		run := "run_" + hex.EncodeToString(id[:])
		s.runs[run] = true
		ok(w, 202, map[string]any{"runId": run})
	case "/api/answer-bank/match":
		s.matches = append(s.matches, body)
		run, _ := body["runId"].(string)
		if !s.runs[run] {
			fail(w, 422, "validation_failed", "runId is not a run this server issued")
			return
		}
		qs, _ := body["questions"].([]any)
		results := make([]any, 0, len(qs))
		for _, q := range qs {
			m := q.(map[string]any)
			item := map[string]any{"selector": m["selector"], "outcome": "abstained", "answer": "", "reason": "no-entry", "confidence": 0, "terminal": false, "trace": []string{}, "clientMayOverwrite": true}
			if strings.Contains(strings.ToLower(m["text"].(string)), "authorized to work") {
				item = map[string]any{"selector": m["selector"], "outcome": "filled", "answer": "Yes", "chosenLabel": "Yes", "entryId": "work-authorization", "confidence": 1, "terminal": false, "trace": []string{}, "clientMayOverwrite": false}
			}
			results = append(results, item)
		}
		ok(w, 200, map[string]any{"results": results})
	case "/api/answer-bank/system-writes":
		if run, _ := body["runId"].(string); !s.runs[run] {
			fail(w, 422, "validation_failed", "runId is not a run this server issued")
			return
		}
		s.writes = append(s.writes, body)
		ok(w, 200, map[string]any{"recorded": true})
	case "/api/answer-bank/events":
		evs, _ := body["events"].([]any)
		for _, e := range evs {
			m := e.(map[string]any)
			if id, _ := m["eventId"].(string); !eventID.MatchString(id) {
				fail(w, 422, "validation_failed", "bad event id")
				return
			}
			s.events = append(s.events, m)
		}
		ok(w, 200, map[string]any{"accepted": len(evs), "duplicates": []string{}, "rejected": []string{}})
	case "/api/browser/sessions/pushes":
		if len(body) != 3 || body["site"] == nil || body["truncated"] == nil {
			fail(w, 422, "validation_failed", "a push holds site, cookies and truncated")
			return
		}
		s.pushes = append(s.pushes, body)
		ok(w, 200, map[string]any{"imported": len(body["cookies"].([]any)), "failed": 0, "replaced": 0, "notInSource": 0, "truncated": false})
	case "/api/submissions/claims":
		if body["url"] == nil || body["host"] == nil || strings.ContainsAny(body["url"].(string), "?#") {
			fail(w, 422, "validation_failed", "a claim holds a reduced url and a host")
			return
		}
		s.presses = append(s.presses, body)
		reply(w, 409, map[string]any{"ok": false, "requestId": "req_stub0001", "error": map[string]any{
			"code": "conflict", "message": "This posting was already submitted.", "retryable": false,
			"details": map[string]any{"reason": "duplicate_submit_blocked"},
		}})
	case "/api/submissions":
		if body["entryId"] == nil || body["url"] == nil || body["host"] == nil || strings.ContainsAny(body["url"].(string), "?#") {
			fail(w, 422, "validation_failed", "a submission holds an entry id, a reduced url and a host")
			return
		}
		s.subs = append(s.subs, body)
		ok(w, 200, map[string]any{"ledgerId": len(s.subs), "result": "created", "tracker": "unchanged"})
	case "/api/browser/sessions/checkin":
		if _, has := body["sites"]; !has || len(body) != 1 {
			fail(w, 422, "validation_failed", "a check-in holds sites only")
			return
		}
		ok(w, 200, map[string]any{"sites": []string{}, "pending": nil})
	default:
		if m := endPath.FindStringSubmatch(path); m != nil {
			if !s.runs[m[1]] || (body["status"] != "succeeded" && body["status"] != "cancelled") {
				fail(w, 422, "validation_failed", "not an issued run or not an outcome")
				return
			}
			s.ends = append(s.ends, m[1]+" "+body["status"].(string))
			ok(w, 200, map[string]any{"runId": m[1], "status": body["status"]})
			return
		}
		fail(w, 404, "not_found", "no such route")
	}
}

// submissions are the submission records the extension posted, oldest first.
func (s *stubAPI) submissions() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.subs...)
}

func (s *stubAPI) pressClaims() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.presses...)
}

func (s *stubAPI) endings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ends...)
}

func (s *stubAPI) snapshot() (writes, events, pushes, matches []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.writes...), append([]map[string]any(nil), s.events...), append([]map[string]any(nil), s.pushes...), append([]map[string]any(nil), s.matches...)
}

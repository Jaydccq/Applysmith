package providerusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshFixedEndpointsAndParsing(t *testing.T) {
	tests := []struct {
		id, host, path, authorization, payload string
		creds                                  Credentials
		windows                                int
		percent                                float64
	}{
		{"minimax-global", "www.minimax.io", "/v1/token_plan/remains", "Bearer demo", `{"model_remains":[{"model_name":"secret-is-not-a-label","current_interval_remaining_percent":75,"current_weekly_remaining_percent":95}]}`, Credentials{Key: "demo", Cookie: "_token=ignored"}, 2, 25},
		{"minimax-cn", "www.minimaxi.com", "/v1/token_plan/remains", "Bearer demo", `{"model_remains":[{"current_interval_remaining_percent":75}]}`, Credentials{Key: "demo"}, 1, 25},
		{"glm-zai", "api.z.ai", "/api/monitor/usage/quota/limit", "demo", `{"success":true,"data":{"limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":25}]}}`, Credentials{Key: "demo"}, 1, 25},
		{"glm-cn", "open.bigmodel.cn", "/api/monitor/usage/quota/limit", "demo", `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","usage":100,"currentValue":25,"unit":6,"number":1}]}}`, Credentials{Key: "demo"}, 1, 25},
		{"opencode-go", "opencode.ai", "/zen/go/v1/usage", "Bearer demo", `{"usage":{"rolling":{"percent":25},"weekly":{"percent":5}}}`, Credentials{Key: "demo"}, 2, 25},
		{"opencode-go", "opencode.ai", "/console/api/go/status", "", `{"access":{"meters":{"fiveHour":{"usedMicroCents":25,"limitMicroCents":100}}}}`, Credentials{Cookie: "auth=demo; __Host-console_session=demo", Workspace: "wrk_demo"}, 1, 25},
	}
	for _, tt := range tests {
		t.Run(tt.id+tt.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path || r.Header.Get("Authorization") != tt.authorization {
					t.Errorf("unexpected path or auth scheme")
				}
				if tt.creds.Key != "" && r.Header.Get("Cookie") != "" {
					t.Error("cookie sent despite preferred key")
				}
				if tt.creds.Workspace != "" && r.Header.Get("x-org-id") != tt.creds.Workspace {
					t.Error("missing workspace")
				}
				io.WriteString(w, tt.payload)
			}))
			defer server.Close()
			local, _ := url.Parse(server.URL)
			client := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != tt.host || r.URL.Scheme != "https" {
					t.Errorf("wrong fixed origin %s", r.URL.Host)
				}
				copy := r.Clone(r.Context())
				u := *r.URL
				u.Scheme = local.Scheme
				u.Host = local.Host
				copy.URL = &u
				return http.DefaultTransport.RoundTrip(copy)
			})}
			d, _ := Lookup(tt.id)
			u := client.Refresh(context.Background(), d, tt.creds)
			if u.Status != "ok" || len(u.Windows) != tt.windows || *u.Windows[0].UsedPercent != tt.percent {
				t.Fatalf("usage: %+v", u)
			}
			body, _ := json.Marshal(u)
			if strings.Contains(string(body), "secret-is-not-a-label") {
				t.Fatal("returned arbitrary provider string")
			}
		})
	}
}
func TestRefreshNeverFollowsRedirectOrReturnsServerSecrets(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	local, _ := url.Parse(redirect.URL)
	client := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = local.Scheme
		u.Host = local.Host
		copy.URL = &u
		return http.DefaultTransport.RoundTrip(copy)
	})}
	d, _ := Lookup("glm-zai")
	u := client.Refresh(context.Background(), d, Credentials{Key: "private"})
	if u.Status != "error" || leaked.Load() != 0 {
		t.Fatalf("redirect followed: %s %d", u.Status, leaked.Load())
	}
	for _, payload := range []string{`{}`, `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT"}]}}`, `{"model_remains":[{"current_interval_total_count":0,"current_interval_usage_count":0}]}`, `<html>private</html>`} {
		c := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
		})}
		u := c.Refresh(context.Background(), d, Credentials{Key: "private"})
		if u.Status != "unknown" || len(u.Windows) != 0 || strings.Contains(u.Message, "private") {
			t.Fatalf("unknown rendered as success or echoed body: %+v", u)
		}
	}
	c := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("private bearer credential") })}
	if u := c.Refresh(context.Background(), d, Credentials{Key: "private"}); strings.Contains(u.Message, "private") {
		t.Fatal("raw error leaked")
	}
}
func TestNativeDetectionDoesNotReadCredentials(t *testing.T) {
	home := t.TempDir()
	d, _ := Lookup("codex")
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid secret content"), 0000); err != nil {
		t.Fatal(err)
	}
	state := DetectNative(d, home, func(name string) (string, error) {
		if name != "codex" {
			t.Fatal("arbitrary command lookup")
		}
		return "/fake/codex", nil
	})
	if !state.Installed || !state.AuthFilePresent {
		t.Fatalf("state %+v", state)
	}
}
func TestCredentialValidationAndCacheInvalidation(t *testing.T) {
	d, _ := Lookup("minimax-global")
	for _, v := range []string{"", "token\r\nX-Extra: bad", strings.Repeat("x", 2049)} {
		if ValidateCredential(d, "key", v) == nil {
			t.Fatal("accepted invalid key")
		}
	}
	if ValidateCredential(d, "cookie", "Cookie: _token=demo; minimax_group_id_v2=123") != nil {
		t.Fatal("valid copied header refused")
	}
	glm, _ := Lookup("glm-zai")
	if ValidateCredential(glm, "cookie", "auth=demo") == nil {
		t.Fatal("accepted unsupported cookie")
	}
	var c Cache
	if !c.Begin("a") || c.Begin("a") {
		t.Fatal("concurrent refresh allowed")
	}
	c.Clear("a")
	c.End("a", Unknown("ok", "stale"))
	if _, ok := c.Get("a"); ok {
		t.Fatal("in-flight old credential result restored after clear")
	}
}

func TestRefreshBoundsResponseBodyAndIgnoresUnknownQuota(t *testing.T) {
	d, _ := Lookup("minimax-global")
	for _, payload := range []string{strings.Repeat("x", (1<<20)+1), `{"model_remains":[{"current_interval_remaining_percent":-1,"current_weekly_remaining_percent":101}]}`, `{"model_remains":[{"current_interval_total_count":0,"current_interval_usage_count":0}]}`} {
		c := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
		})}
		got := c.Refresh(context.Background(), d, Credentials{Key: "demo"})
		if got.Status == "ok" || len(got.Windows) != 0 {
			t.Fatalf("invalid or legacy zero quota rendered as known: %+v", got)
		}
	}
}

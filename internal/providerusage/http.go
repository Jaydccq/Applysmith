package providerusage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client supplies a bounded, redirect-free transport. URLs are constructed only
// from the fixed provider table; no request body or account metadata selects a host.
type Client struct{ transport http.RoundTripper }

func NewClient() *Client { return &Client{transport: http.DefaultTransport} }

type Credentials struct{ Key, Cookie, Workspace string }
type httpCookieValidator struct{ value string }

func (v *httpCookieValidator) valid() bool {
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("Cookie", strings.TrimPrefix(v.value, "Cookie:"))
	return len(r.Cookies()) > 0
}
func (c *Client) Refresh(ctx context.Context, d Definition, creds Credentials) Usage {
	if d.Mode != "subscription" {
		u := Unknown("unsupported", "Native account and subscription usage are managed by the provider; Applysmith does not import that login.")
		u.CheckedAt = Now()
		return u
	}
	if creds.Key == "" && creds.Cookie == "" {
		return Unknown("unconfigured", "Save a plan credential before refreshing.")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	endpoint := ""
	headers := http.Header{"Accept": []string{"application/json"}}
	switch d.ID {
	case "minimax-global", "minimax-cn":
		host := "www.minimax.io"
		if d.ID == "minimax-cn" {
			host = "www.minimaxi.com"
		}
		endpoint = "https://" + host + "/v1/token_plan/remains"
		if creds.Key == "" {
			endpoint = "https://platform.minimax.io/v1/api/openplatform/coding_plan/remains"
			if d.ID == "minimax-cn" {
				endpoint = "https://www.minimaxi.com/v1/api/openplatform/coding_plan/remains"
			}
			headers.Set("Cookie", strings.TrimSpace(strings.TrimPrefix(creds.Cookie, "Cookie:")))
			r := &http.Request{Header: headers}
			for _, cookie := range r.Cookies() {
				if cookie.Name == "minimax_group_id_v2" {
					headers.Set("X-Group-Id", cookie.Value)
				}
			}
		} else {
			headers.Set("Authorization", "Bearer "+creds.Key)
		}
	case "glm-zai", "glm-cn":
		host := "api.z.ai"
		if d.ID == "glm-cn" {
			host = "open.bigmodel.cn"
		}
		endpoint = "https://" + host + "/api/monitor/usage/quota/limit"
		headers.Set("Authorization", creds.Key)
	case "opencode-go":
		endpoint = "https://opencode.ai/zen/go/v1/usage"
		if creds.Key != "" {
			headers.Set("Authorization", "Bearer "+creds.Key)
		} else {
			if !workspaceID.MatchString(creds.Workspace) {
				return Unknown("unconfigured", "Save the legacy console workspace ID (wrk_…) or use an OpenCode Go API key.")
			}
			endpoint = "https://opencode.ai/console/api/go/status"
			headers.Set("Cookie", strings.TrimSpace(strings.TrimPrefix(creds.Cookie, "Cookie:")))
			headers.Set("x-org-id", creds.Workspace)
		}
	default:
		return Unknown("unsupported", "Usage monitoring is not available for this provider.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Unknown("error", "Could not create the usage request.")
	}
	req.Header = headers
	client := &http.Client{Transport: c.transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	u := Unknown("error", "Usage request failed; check your connection and try again.")
	u.CheckedAt = Now()
	if err != nil {
		return u
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		u.Message = "The credential was rejected or this account has no active plan. Replace it or check the provider console."
		return u
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		u.Message = "The provider did not return usage. Check the provider console and try again."
		return u
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		u.Message = "The provider returned an unreadable or oversized usage response."
		return u
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		u.Status = "unknown"
		u.Message = "The provider returned an unrecognized usage response."
		return u
	}
	var windows []Window
	switch d.ID {
	case "minimax-global", "minimax-cn":
		windows = parseMiniMax(data)
	case "glm-zai", "glm-cn":
		windows = parseGLM(data)
	case "opencode-go":
		windows = parseOpenCode(data, creds.Key != "")
	}
	if len(windows) == 0 {
		u.Status = "unknown"
		u.Message = "No supported quota data was returned. Usage is unknown; check the provider console."
		return u
	}
	u.Status = "ok"
	u.Message = "Subscription quota from the provider (separate from Applysmith request costs)."
	u.Windows = windows
	return u
}

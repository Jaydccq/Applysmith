package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/gates"
)

const (
	defaultBaseURL   = "https://api.2captcha.com"
	maxResponseBytes = 64 << 10
	maxTokenBytes    = 32 << 10
	requestTimeout   = 20 * time.Second

	codeMalformed = "ERROR_MALFORMED_RESPONSE"
	codeNetwork   = "ERROR_NETWORK"
)

// ServiceError is a failure of the 2Captcha API. Its text is built from the
// service's error code and the HTTP status only (gates.CaptchaErrorLine), so
// no key, token or page address can ride along in an error.
type ServiceError struct {
	Code   string
	Status int
}

func (e *ServiceError) Error() string { return gates.CaptchaErrorLine(e.Code, e.Status) }

// serviceError keeps a reply's error code only if it has the shape of a code,
// so the struct itself can never hold text the service chose.
func serviceError(code string, status int) *ServiceError {
	ok := code != "" && len(code) <= 64
	for _, r := range code {
		ok = ok && (r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}
	if !ok {
		code = "ERROR_UNKNOWN"
	}
	return &ServiceError{Code: code, Status: status}
}

// taskTypes maps each solvable challenge to the service's proxyless task.
var taskTypes = map[string]string{
	gates.ChallengeRecaptchaV2:           "RecaptchaV2TaskProxyless",
	gates.ChallengeRecaptchaV2Enterprise: "RecaptchaV2EnterpriseTaskProxyless",
	gates.ChallengeHCaptcha:              "HCaptchaTaskProxyless",
	gates.ChallengeTurnstileWidget:       "TurnstileTaskProxyless",
}

type api struct {
	base string
	http *http.Client
	key  func() string
}

type envelope struct {
	ErrorID   *int   `json:"errorId"`
	ErrorCode string `json:"errorCode"`
}

// post sends one JSON request and decodes the reply into out, which embeds
// envelope. Anything that does not look like the documented reply is a
// malformed-response failure, never a partial success.
func (a *api) post(ctx context.Context, path string, body map[string]any, out any) error {
	body["clientKey"] = strings.TrimSpace(a.key())
	payload, err := json.Marshal(body)
	if err != nil {
		return &ServiceError{Code: codeMalformed}
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, a.base+path, bytes.NewReader(payload))
	if err != nil {
		return &ServiceError{Code: codeNetwork}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ServiceError{Code: codeNetwork}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ServiceError{Code: codeNetwork, Status: resp.StatusCode}
	}
	if len(raw) > maxResponseBytes {
		return &ServiceError{Code: codeMalformed, Status: resp.StatusCode}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.ErrorID == nil {
		return &ServiceError{Code: codeMalformed, Status: resp.StatusCode}
	}
	if *env.ErrorID != 0 || resp.StatusCode != http.StatusOK {
		return serviceError(env.ErrorCode, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &ServiceError{Code: codeMalformed, Status: resp.StatusCode}
	}
	return nil
}

// balanceMicroUSD reads the account balance in millionths of a dollar.
func (a *api) balanceMicroUSD(ctx context.Context) (int64, error) {
	var r struct {
		Balance *float64 `json:"balance"`
	}
	if err := a.post(ctx, "/getBalance", map[string]any{}, &r); err != nil {
		return 0, err
	}
	if r.Balance == nil || math.IsNaN(*r.Balance) || *r.Balance < 0 {
		return 0, &ServiceError{Code: codeMalformed, Status: http.StatusOK}
	}
	// Round down with no tolerance: a balance a hair under the floor is under
	// it. A figure too large for int64 saturates and the gate refuses it as
	// implausible.
	micro := math.Floor(*r.Balance * 1e6)
	if micro >= math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(micro), nil
}

// Balance reads the account balance in millionths of a dollar with one
// request, for the dashboard's key test. Only cfg.Key matters, and BaseURL and
// HTTP in tests.
func Balance(ctx context.Context, cfg Config) (int64, error) {
	return New(cfg).api.balanceMicroUSD(ctx)
}

// KeyRejected reports a service error that says the key itself is wrong.
func KeyRejected(err error) bool {
	var se *ServiceError
	return errors.As(err, &se) && (se.Code == "ERROR_KEY_DOES_NOT_EXIST" || se.Code == "ERROR_WRONG_USER_KEY")
}

// createTask opens a solve and returns its id.
func (a *api) createTask(ctx context.Context, c Challenge) (int64, error) {
	task := map[string]any{"type": taskTypes[c.Kind], "websiteURL": c.PageURL, "websiteKey": c.SiteKey}
	var r struct {
		TaskID *int64 `json:"taskId"`
	}
	if err := a.post(ctx, "/createTask", map[string]any{"task": task}, &r); err != nil {
		return 0, err
	}
	if r.TaskID == nil || *r.TaskID <= 0 {
		return 0, &ServiceError{Code: codeMalformed, Status: http.StatusOK}
	}
	return *r.TaskID, nil
}

// taskResult reads a task once. A pending task returns "" and no error.
func (a *api) taskResult(ctx context.Context, kind string, id int64) (string, error) {
	var r struct {
		Status   string `json:"status"`
		Solution struct {
			GRecaptchaResponse string `json:"gRecaptchaResponse"`
			Token              string `json:"token"`
		} `json:"solution"`
	}
	if err := a.post(ctx, "/getTaskResult", map[string]any{"taskId": id}, &r); err != nil {
		return "", err
	}
	switch r.Status {
	case "processing":
		return "", nil
	case "ready":
		token := r.Solution.GRecaptchaResponse
		if kind == gates.ChallengeTurnstileWidget {
			token = r.Solution.Token
		}
		if !plausibleToken(token) {
			return "", &ServiceError{Code: codeMalformed, Status: http.StatusOK}
		}
		return token, nil
	}
	return "", &ServiceError{Code: codeMalformed, Status: http.StatusOK}
}

func plausibleToken(t string) bool {
	if t == "" || len(t) > maxTokenBytes {
		return false
	}
	return !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

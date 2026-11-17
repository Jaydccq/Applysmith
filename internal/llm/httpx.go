package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const maxErrBody = 4096

// post sends a JSON body and returns the response; a non-2xx status becomes a
// classified *Error with the redacted, bounded body.
func (r *Router) post(ctx context.Context, url string, headers map[string]string, body any, key string) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, &Error{Kind: KindOther, Message: Redact(err.Error(), key)}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		e := classifyHTTP(resp.StatusCode, boundedText(string(b), key))
		e.retryAfter = retryHint(resp.Header.Get("Retry-After"), string(b), time.Now())
		return nil, e
	}
	return resp, nil
}

var bodyRetry = lazyregexp.New(`(?i)"resets_in_seconds"\s*:\s*(\d+)|"retryDelay"\s*:\s*"(\d+)s"|try again in (\d+)\s*(?:s\b|sec)`)

// retryHint is how many seconds the server said to wait: the Retry-After
// header (seconds or an HTTP date), else a reset time named in the body. Zero
// when it said nothing.
func retryHint(header, body string, now time.Time) int {
	header = strings.TrimSpace(header)
	if n, err := strconv.Atoi(header); err == nil && n > 0 {
		return n
	}
	if t, err := http.ParseTime(header); err == nil && t.After(now) {
		return int(t.Sub(now).Round(time.Second) / time.Second)
	}
	if m := bodyRetry.FindStringSubmatch(body); m != nil {
		for _, g := range m[1:] {
			if n, err := strconv.Atoi(g); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// readSSE calls fn with the payload of every data line. A line that is not a
// data line (a comment or an event name) is not a stream event.
func readSSE(body io.Reader, fn func(data string) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		if err := fn(strings.TrimSpace(data)); err != nil {
			return err
		}
	}
	return sc.Err()
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

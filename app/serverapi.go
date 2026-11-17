package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/egoist/mygo"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
)

// serverError is a refusal from the server's API.
type serverError struct {
	Message string
	Reason  string
}

func (e *serverError) Error() string { return e.Message }

// chromeMissing is the reason the server gives when no Chrome can be found.
const chromeMissing = "chrome-missing"

// readServer reads a bounded authenticated snapshot without following redirects.
func (a *shell) readServer(ctx context.Context, path string, out any) error {
	a.mu.Lock()
	origin, token := a.origin, a.token
	a.mu.Unlock()
	if origin == "" || token == nil {
		return errNoServer
	}
	tok, err := token()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server snapshot: HTTP %d", resp.StatusCode)
	}
	var reply struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return err
	}
	if !reply.OK || len(reply.Data) == 0 || string(reply.Data) == "null" {
		return errors.New("server snapshot has no data")
	}
	return json.Unmarshal(reply.Data, out)
}

// callServer posts an optional JSON body to the server this shell shows, with the local
// API token. The token is read on every call so that a rotated one is used, and
// a redirect is not followed.
func (a *shell) callServer(ctx context.Context, path string, body any) error {
	_, err := a.postServer(ctx, path, body)
	return err
}

// postServer is callServer that also returns the reply body.
func (a *shell) postServer(ctx context.Context, path string, body any) ([]byte, error) {
	a.mu.Lock()
	origin, token := a.origin, a.token
	a.mu.Unlock()
	if origin == "" || token == nil {
		return nil, errNoServer
	}
	tok, err := token()
	if err != nil {
		return nil, fmt.Errorf("api token: %w", err)
	}
	var raw []byte
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		return data, nil
	}
	var f struct {
		Error struct {
			Message string `json:"message"`
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &f)
	if f.Error.Message == "" {
		f.Error.Message = resp.Status
	}
	return nil, &serverError{Message: f.Error.Message, Reason: f.Error.Details.Reason}
}

// freshDashboardURL mints a launch code and returns the address that redeems
// it for a session.
func (a *shell) freshDashboardURL(ctx context.Context) (string, error) {
	data, err := a.postServer(ctx, "/api/launch-codes", nil)
	if err != nil {
		return "", err
	}
	var reply struct {
		Data wire.LaunchCode `json:"data"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.Data.Code == "" {
		return "", errors.New("launch code not issued")
	}
	a.mu.Lock()
	origin := a.origin
	a.mu.Unlock()
	return api.DashboardURL(origin, reply.Data.Code), nil
}

// showApplyWindow asks the server to bring the apply window forward. A
// missing Chrome gets a notice that leads to the setup guide.
func (a *shell) showApplyWindow(ctx context.Context) error {
	err := a.callServer(ctx, "/api/browser/apply-window/show", struct{}{})
	var se *serverError
	if errors.As(err, &se) && se.Reason == chromeMissing {
		a.chromeNotice()
	}
	return err
}

// chromeNotice explains that the apply window needs Chrome and offers the
// setup step that covers it.
func (a *shell) chromeNotice() {
	a.mu.Lock()
	w, origin := a.win, a.origin
	a.mu.Unlock()
	pick := a.desk.notice(w, "Google Chrome was not found",
		"The apply window runs in Google Chrome 120 or later. Install it, then try again. The setup guide has the steps.",
		[]string{"Open Setup Guide", "OK"})
	if pick == 0 && w != nil && origin != "" {
		go a.openPage(w.Page(), origin+"/setup?step=browser")
	}
}

// openLink opens a web or mail address in the default program. A refused
// address is logged without its query; a failure to launch shows a notice with
// the address and a copy action.
func (a *shell) openLink(raw string) error {
	label, err := checkLink(raw)
	if err != nil {
		a.logger().Warn("external link refused", "link", label)
		return err
	}
	if err := a.desk.openExternal(raw); err != nil {
		a.logger().Warn("external link not opened", "link", label)
		a.mu.Lock()
		w := a.win
		a.mu.Unlock()
		if a.desk.notice(w, "The link could not be opened", raw, []string{"Copy Address", "OK"}) == 0 {
			a.desk.copyText(raw)
		}
		return err
	}
	return nil
}

func (a *shell) logger() *slog.Logger {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.log != nil {
		return a.log
	}
	return slog.Default()
}

// lockToOrigin keeps the window on the server's pages. A foreign navigation or
// window.open goes through the external-link rules instead.
func (a *shell) lockToOrigin(w *mygo.Window, origin string) {
	p := w.Page()
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if sameOrigin(e.URL, origin) {
			return
		}
		e.PreventDefault()
		go func() { _ = a.openLink(e.URL) }()
	})
	p.SetWindowOpenHandler(func(r mygo.WindowOpenRequest) *mygo.WindowOptions {
		if sameOrigin(r.URL, origin) {
			go a.openPage(w.Page(), r.URL)
			return nil
		}
		go func() { _ = a.openLink(r.URL) }()
		return nil
	})
	// No camera, microphone or location for any page.
	p.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
}

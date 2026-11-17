package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// A client that went away is not a defect of the handler.
func TestClientDisconnectIsNotAnUnexpectedFailure(t *testing.T) {
	for _, err := range []error{context.Canceled, fmt.Errorf("write inbox row: %w", context.Canceled)} {
		ae, unexpected := asError(err)
		if unexpected || ae.Code == wire.CodeInternal {
			t.Errorf("%v became %s (unexpected=%v)", err, ae.Code, unexpected)
		}
	}
}

// A full disk is the owner's to fix, not a defect: the caller is told so and
// may retry, and the failure is still logged.
func TestDiskFullTellsTheCallerToFreeSpace(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w: database or disk is full (13)", store.ErrDiskFull),
		fmt.Errorf("save narrative: %w", &os.PathError{Op: "write", Path: "narrative.md", Err: syscall.ENOSPC}),
	} {
		ae, unexpected := asError(err)
		if ae.Code != wire.CodeUnavailable || !ae.Retryable || !strings.Contains(ae.Message, "disk is full") || !unexpected {
			t.Errorf("%v became %s %q retryable=%v logged=%v", err, ae.Code, ae.Message, ae.Retryable, unexpected)
		}
	}
}

// callWithGoneClient runs a handler for a request whose client has left.
func callWithGoneClient(handler apikit.Handler, p apikit.Principal, body any) error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	_, err := handler(&apikit.Call{W: httptest.NewRecorder(), R: req, P: p, Body: jsonBody(body)})
	return err
}

// A write whose client left mid-request is not the caller's invalid input or a
// conflict, and the cancellation's text does not reach the caller.
func TestACancelledWriteIsNotAValidationErrorOrAConflict(t *testing.T) {
	h := newDomainHarness(t)
	p := apikit.Principal{Kind: apikit.PrincipalLocalToken, ID: string(apikit.PrincipalLocalToken)}
	for name, handler := range map[string]struct {
		h    apikit.Handler
		body any
	}{
		"block":     {h.srv.blockCompany, map[string]any{"name": "Example Co"}},
		"daily cap": {h.srv.models.PatchRouting, map[string]any{"dailyUsdCap": 3}},
		"routes":    {h.srv.models.PatchRouting, map[string]any{"routes": map[string]any{"quick-screen": nil}}},
	} {
		err := callWithGoneClient(handler.h, p, handler.body)
		if err == nil {
			t.Errorf("%s: no error from a cancelled request", name)
			continue
		}
		ae, unexpected := asError(err)
		if unexpected || ae.Code == wire.CodeValidationFailed || ae.Code == wire.CodeConflict || strings.Contains(ae.Message, "context") {
			t.Errorf("%s: %s %q (unexpected=%v)", name, ae.Code, ae.Message, unexpected)
		}
	}
}

func TestBlockingAnUnnamedCompanyIsStillAValidationError(t *testing.T) {
	h := newDomainHarness(t)
	for _, name := range []string{"", "!!!"} {
		if r := h.post("/api/blocked-companies", map[string]any{"name": name}); r.StatusCode != 422 {
			t.Errorf("%q: %d %s", name, r.StatusCode, r.body)
		}
	}
}

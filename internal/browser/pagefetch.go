package browser

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// consoleRecorderScript keeps, as text, what the page writes to its console,
// in a list ConsoleLines reads back.
const consoleRecorderScript = `(() => {
  if (window.__applysmithConsole) return;
  const out = [];
  window.__applysmithConsole = out;
  const text = (v) => { if (typeof v === 'string') return v; try { return JSON.stringify(v); } catch (e) { return String(v); } };
  for (const k of ['log', 'info', 'debug', 'warn']) {
    const f = console[k];
    console[k] = function (...a) { try { out.push(a.map(text).join(' ')); } catch (e) {} return f.apply(this, a); };
  }
})()`

// ConsoleLines opens rawURL in a background tab of its own, owned by owner,
// records what the page writes to its console while it loads and for settle
// after, and closes the tab. A careers page may print an application code
// there for applicants to copy (Metabase). Only http and https addresses are
// opened.
func (h *LaneHandle) ConsoleLines(ctx context.Context, owner, rawURL string, settle time.Duration) ([]string, error) {
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, newError(CodeSchemeNotAllowed, "only http and https addresses can be opened")
	}
	id, err := h.OpenTab(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = h.CloseTab(context.WithoutCancel(ctx), id) }()
	hooks, err := h.Hooks(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := hooks.AddInitScript(ctx, consoleRecorderScript); err != nil {
		return nil, err
	}
	if _, err := h.Navigate(ctx, id, rawURL); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(settle):
	}
	raw, err := h.Evaluate(ctx, id, `(window.__applysmithConsole || []).slice()`)
	if err != nil {
		return nil, err
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, newError(CodeEvalFailed, "the console record returned an unexpected shape")
	}
	return lines, nil
}

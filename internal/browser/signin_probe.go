package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	// probeDeadline bounds one site's probe, load and read together.
	probeDeadline = 30 * time.Second
	// probeFresh is how long a signed-in or rate-limited result of a site
	// answers a new probe.
	probeFresh = 5 * time.Minute
	// probeTextLimit caps the page text the classifier reads.
	probeTextLimit = 20000
)

// readPageJS reads the loaded document without touching it: no typing, no
// clicking, no scrolling.
const readPageJS = `(()=>{
const vis=e=>{const r=e.getBoundingClientRect();const s=getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.display!=='none'};
const pw=[...document.querySelectorAll('input[type=password]')].some(vis);
const cta=[...document.querySelectorAll('a,button,input[type=submit]')].some(e=>vis(e)&&/^\s*(sign\s*in|log\s*in|login)\s*$/i.test((e.innerText||e.value||'').trim()));
return {title:document.title||'',text:(document.body?document.body.innerText:'').slice(0,20000),signInForm:pw||cta,frames:[...document.querySelectorAll('iframe')].map(f=>f.src||'')};
})()`

// readSignInPage loads the site's account page in a scan tab and reads it. It
// only loads and reads; the tab is closed on every path.
func (i *Instance) readSignInPage(ctx context.Context, site SignInSite) (PageReading, error) {
	ctx, cancel := context.WithTimeout(ctx, probeDeadline)
	defer cancel()
	h := i.Lane(LaneScan)
	id, err := h.OpenTab(ctx, "signin-probe")
	if err != nil {
		return PageReading{}, err
	}
	defer func() { _ = h.CloseTab(context.WithoutCancel(ctx), id) }()
	nav, err := h.Navigate(ctx, id, site.AccountURL)
	if err != nil {
		return PageReading{}, err
	}
	if nav.Error != "" {
		return PageReading{LoadFailed: true}, nil
	}
	raw, err := h.Evaluate(ctx, id, readPageJS)
	if err != nil {
		return PageReading{}, err
	}
	var doc struct {
		Title      string   `json:"title"`
		Text       string   `json:"text"`
		SignInForm bool     `json:"signInForm"`
		Frames     []string `json:"frames"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return PageReading{}, fmt.Errorf("browser: the probe read an unexpected page shape: %w", err)
	}
	return PageReading{Status: nav.Status, URL: nav.URL, Title: firstNonEmptyStr(doc.Title, nav.Title), Text: truncate(doc.Text, probeTextLimit), SignInForm: doc.SignInForm, Frames: doc.Frames}, nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SignInRecord is a stored probe result.
type SignInRecord struct {
	State     SignInState
	CheckedAt time.Time
}

// SignInStore keeps the last probe result of each site.
type SignInStore interface {
	Last(ctx context.Context) (map[string]SignInRecord, error)
	Save(ctx context.Context, domain string, state SignInState, at time.Time) error
}

// SignInCheck is the outcome of probing one site.
type SignInCheck struct {
	Site      SignInSite
	State     SignInState
	CheckedAt time.Time
	// Cached is true when a signed-in or rate-limited result under five
	// minutes old answered and no page was loaded.
	Cached bool
	// Err says why a probe could not run; the stored state is then unchanged.
	Err error
}

// CheckSignIn probes the listed sites named by ids (all of them when ids is
// empty), one after another, each within 30 seconds. A site signed in or
// rate-limited under five minutes ago is answered from that result; any other
// state is what the user signs in or clears a challenge to change, so it is
// read again. A state is stored with the time the page was read.
func (s *Sessions) CheckSignIn(ctx context.Context, ids ...string) ([]SignInCheck, error) {
	if s.cfg.Probe == nil || s.cfg.States == nil {
		return nil, errors.New("browser: the sign-in probe is not configured")
	}
	last, err := s.cfg.States.Last(ctx)
	if err != nil {
		return nil, err
	}
	var out []SignInCheck
	for _, site := range SignInSites {
		if len(ids) > 0 && !slices.Contains(ids, site.ID) {
			continue
		}
		if rec, ok := last[site.Domain]; ok && (rec.State == StateSignedIn || rec.State == StateRateLimited) && s.now().Sub(rec.CheckedAt) < probeFresh && s.now().Sub(rec.CheckedAt) >= 0 {
			out = append(out, SignInCheck{Site: site, State: rec.State, CheckedAt: rec.CheckedAt, Cached: true})
			continue
		}
		reading, perr := s.cfg.Probe(ctx, site)
		if perr != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out = append(out, SignInCheck{Site: site, State: stateOf(last, site), Err: perr})
			continue
		}
		reading.Cookies = s.cookieNames(ctx, site)
		state := ClassifySignIn(site, reading)
		at := s.now().UTC()
		if err := s.cfg.States.Save(ctx, site.Domain, state, at); err != nil {
			return out, err
		}
		out = append(out, SignInCheck{Site: site, State: state, CheckedAt: at})
	}
	return out, nil
}

func stateOf(last map[string]SignInRecord, site SignInSite) SignInState {
	if rec, ok := last[site.Domain]; ok {
		return rec.State
	}
	return StateUnknown
}

// cookieNames lists the names of the site's cookies in the running profile,
// for a site whose rule names proof cookies; no other rule reads them.
func (s *Sessions) cookieNames(ctx context.Context, site SignInSite) []string {
	if len(site.Cookies) == 0 || s.cfg.Running == nil {
		return nil
	}
	jar := s.cfg.Running()
	if jar == nil {
		return nil
	}
	cookies, err := jar.Cookies(ctx)
	if err != nil {
		return nil
	}
	var names []string
	for _, c := range cookies {
		if scoped(c.host(), site.Domain) {
			names = append(names, c.Name)
		}
	}
	return names
}

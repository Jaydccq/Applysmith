package api

import (
	"cmp"
	"context"
	"fmt"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/notify"
)

// siteSignInID prefixes the inbox item of a job site the app browser is
// signed out of; the site's domain follows.
const siteSignInID = "site-sign-in-"

// leaveSignedOut leaves out of a list the rows on a job site whose sign-in
// cookies the app browser lacks, once a sync from the user's Chrome did not
// bring them, and says so in the inbox as the list starts: such a row only
// stops at the site's signed-out page, possibly an hour into the list. A site
// found signed in resolves its item.
func (s *Server) leaveSignedOut(ctx context.Context, rows []agent.ApplyRow, skipped []wire.ApplySkippedRow) ([]agent.ApplyRow, []wire.ApplySkippedRow) {
	b := s.sessionBrowser()
	out := map[string]*browser.SignInSite{}
	left := map[string]int{}
	silent := false
	keep := rows[:0]
	for _, r := range rows {
		// A row the list resolved to the employer's posting runs there.
		host := hostOf(cmp.Or(r.EmployerURL, r.URL))
		site, listed := browser.SiteOf(host)
		if !listed {
			keep = append(keep, r)
			continue
		}
		v, seen := out[site.Domain]
		if !seen {
			if _, signedOut := b.SignedOut(ctx, host); signedOut {
				got, err := s.SyncOwnHost(ctx, host)
				switch {
				case err == nil && got.Status == OwnHostImported:
					_, signedOut = b.SignedOut(ctx, host)
				case err == nil && got.Reason == reasonExtensionSilent:
					silent = true
				}
				if signedOut {
					v = &site
				}
			}
			out[site.Domain] = v
		}
		if v == nil {
			keep = append(keep, r)
			continue
		}
		left[site.Domain]++
		skipped = append(skipped, wire.ApplySkippedRow{Company: r.Company, Role: r.Role,
			Reason: "the app browser is signed out of " + v.Label + "; sign in on the Browser page, then run it again"})
	}
	for domain, site := range out {
		if site == nil {
			s.siteSignedIn(ctx, domain)
			continue
		}
		s.siteSignedOut(ctx, *site, left[domain], silent)
	}
	return keep, skipped
}

// siteSignedOut puts one item per site in the inbox; a later list finds it
// waiting, brings its words up to date and does not tell it again. With no
// extension answering in the user's Chrome it says where one is paired: the
// sync from Chrome is what spares the user signing in twice.
func (s *Server) siteSignedOut(ctx context.Context, site browser.SignInSite, rows int, silent bool) {
	ns, err := s.notifications()
	if err != nil {
		return
	}
	n := "1 row"
	if rows != 1 {
		n = fmt.Sprintf("%d rows", rows)
	}
	detail := fmt.Sprintf("The app browser is signed out of %s, so a list left out %s on it. Sign in on the Browser page, then run the list again.", site.Label, n)
	if silent {
		detail += " No Applysmith extension is paired in your Chrome to copy its sign-in (Settings, Browser extension)."
	}
	id := siteSignInID + site.Domain
	e, err := ns.board.Raise(ctx, notify.RaiseArgs{ID: id, Kind: notify.KindTask, Origin: notify.OriginUser,
		Title: "Sign in to " + site.Label + " in the app browser", State: notify.NeedsYou, Reason: "sign-in", Link: "/browser", Detail: detail})
	if err == nil && e.Detail != detail {
		_, err = ns.board.Update(ctx, id, notify.Patch{Detail: &detail})
	}
	if err != nil {
		s.log.Warn("the site sign-in item could not be raised", "site", site.Domain, "error", err.Error())
	}
}

// siteSignedIn resolves a site's sign-in item.
func (s *Server) siteSignedIn(ctx context.Context, domain string) {
	ns, err := s.notifications()
	if err != nil {
		return
	}
	if err := ns.board.Resolve(ctx, siteSignInID+domain); err != nil {
		s.log.Warn("the site sign-in item could not be resolved", "site", domain, "error", err.Error())
	}
}

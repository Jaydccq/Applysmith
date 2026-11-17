package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestLinkKind_Table(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://bit.ly/3abc", LinkShortener},
		{"https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001", LinkATSJob},
		{"https://forms.gle/abc", LinkApplicationForm},
		{"https://jobs.ashbyhq.com/example-corp", LinkCareersIndex},
		{"https://www.example.org/careers", LinkCareersIndex},
		{"https://www.example.org/", LinkCompanyHome},
		{"https://www.example.org/job/12345", LinkJobDetail},
		{"https://www.example.org/about", LinkOther},
		{"mailto:jobs@example.org", LinkOther},
	}
	for _, c := range cases {
		if got := LinkKind(c.url); got != c.want {
			t.Errorf("LinkKind(%q) = %s, want %s", c.url, got, c.want)
		}
	}
	for kind, want := range map[string]bool{LinkATSJob: true, LinkJobDetail: true, LinkApplicationForm: true, LinkCareersIndex: false, LinkShortener: false, LinkOther: false} {
		if SinglePostingLink(kind) != want {
			t.Errorf("SinglePostingLink(%s) != %v", kind, want)
		}
	}
}

func TestLinkKind_SingleVersusIndexRanking(t *testing.T) {
	links := []string{"https://www.example.org/careers", "https://bit.ly/x", "https://www.example.org/", "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001"}
	got := RankLinks(links)
	want := []string{links[3], links[0], links[2]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked = %v, want %v", got, want)
	}
}

func TestExtractEmails(t *testing.T) {
	got := ExtractEmails("Apply to Jobs@Example.org, or mailto:hr@example.org. Not an address: foo@bar")
	if !reflect.DeepEqual(got, []string{"jobs@example.org", "hr@example.org"}) {
		t.Fatalf("emails = %v", got)
	}
}

// shortener serves a chain of redirects on logical hosts.
func shortenerClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	return &http.Client{Transport: &rewriteTransport{target: target}}
}

func TestExpandShortener_FollowsRedirectsToClassifiedLink(t *testing.T) {
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Original-Host") + r.URL.Path {
		case "bit.ly/abc":
			http.Redirect(w, r, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001", http.StatusFound)
		default:
			w.WriteHeader(200)
		}
	})
	ex := &Expander{HTTP: hc}
	final, ok := ex.Expand(context.Background(), "https://bit.ly/abc")
	if !ok || LinkKind(final) != LinkATSJob {
		t.Fatalf("expanded to %q, %v", final, ok)
	}
}

func TestExpandShortener_StopsAfterThreeHops(t *testing.T) {
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		hosts := map[string]string{"bit.ly": "https://a.example.org/1", "a.example.org": "https://b.example.org/2", "b.example.org": "https://c.example.org/3", "c.example.org": "https://d.example.org/4", "d.example.org": "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001"}
		if next, ok := hosts[r.Header.Get("X-Original-Host")]; ok {
			http.Redirect(w, r, next, http.StatusFound)
		}
	})
	ex := &Expander{HTTP: hc}
	final, ok := ex.Expand(context.Background(), "https://bit.ly/abc")
	if ok || final != "https://bit.ly/abc" || LinkKind(final) != LinkShortener || SinglePostingLink(LinkKind(final)) {
		t.Fatalf("expanded to %q, %v", final, ok)
	}
}

func TestExpandShortener_MetaRefreshAndScriptRedirect(t *testing.T) {
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Original-Host") {
		case "bit.ly":
			writeHTML(w, `<html><head><meta http-equiv="refresh" content="0; url=https://hop.example.org/x"></head></html>`)
		case "hop.example.org":
			writeHTML(w, `<script>window.location = "https://job-boards.greenhouse.io/example-co/jobs/4001";</script>`)
		}
	})
	final, ok := (&Expander{HTTP: hc}).Expand(context.Background(), "https://bit.ly/abc")
	if !ok || final != "https://job-boards.greenhouse.io/example-co/jobs/4001" {
		t.Fatalf("expanded to %q, %v", final, ok)
	}
}

func TestExpandShortener_LinkedInLeavePage(t *testing.T) {
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Original-Host") == "lnkd.in" {
			writeHTML(w, `<body data-recaptcha-v3-integration-lix-value="control"><a class="artdeco-button" data-tracking-control-name="external_url_click" data-tracking-will-navigate href="https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001?a=1&amp;b=2">x</a>`)
		}
	})
	final, ok := (&Expander{HTTP: hc}).Expand(context.Background(), "https://lnkd.in/abc")
	if !ok || final != "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001?a=1&b=2" {
		t.Fatalf("expanded to %q, %v", final, ok)
	}
}

func TestExpandShortener_LoginWallStopsExpansion(t *testing.T) {
	var asked []string
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Header.Get("X-Original-Host")+r.URL.Path)
		if r.Header.Get("X-Original-Host") == "bit.ly" {
			http.Redirect(w, r, "https://www.linkedin.com/login?session_redirect=x", http.StatusFound)
		}
	})
	final, ok := (&Expander{HTTP: hc}).Expand(context.Background(), "https://bit.ly/abc")
	if ok || final != "https://bit.ly/abc" {
		t.Fatalf("expanded to %q, %v", final, ok)
	}
	for _, a := range asked {
		if a != "bit.ly/abc" {
			t.Errorf("the login page was requested: %v", asked)
		}
	}
}

func TestExpandShortener_FiveExpansionsPerLead(t *testing.T) {
	hc := shortenerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Original-Host") == "bit.ly" {
			http.Redirect(w, r, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001", http.StatusFound)
		}
	})
	ex := &Expander{HTTP: hc}
	var oks int
	for i := 0; i < 7; i++ {
		if _, ok := ex.Expand(context.Background(), "https://bit.ly/abc"); ok {
			oks++
		}
	}
	if oks != 5 {
		t.Fatalf("%d expansions, want 5", oks)
	}
	if _, ok := ex.Expand(context.Background(), "https://www.example.org/careers"); ok {
		t.Fatal("a link that is no shortener was expanded")
	}
}

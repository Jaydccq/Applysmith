package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/store"
)

func TestLeadsCLI_ListExpandAndDismiss(t *testing.T) {
	te := newTestEnv(t)
	srv := atsBoards(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if err := os.WriteFile(filepath.Join(te.ws, "portals.yml"), []byte(portalsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	te.withStore(func(st *store.Store) {
		repo := discovery.NewRepo(st, nil)
		if err := repo.AddCareersPage(context.Background(), "https://job-boards.greenhouse.io/example-tracked", "Example Tracked", "exa"); err != nil {
			t.Fatal(err)
		}
	})
	list := te.discovery(srv, "leads", "list")
	if list.code != 0 || !strings.Contains(list.stdout, "careers-page") || !strings.Contains(list.stdout, "Example Tracked") {
		t.Fatalf("list: %+v", list)
	}
	exp := te.discovery(srv, "leads", "expand")
	if exp.code != 0 || !strings.Contains(exp.stdout, "1 careers pages read") || !strings.Contains(exp.stdout, "1 added to the inbox") {
		t.Fatalf("expand: %+v", exp)
	}
	again := te.discovery(srv, "leads", "expand")
	if !strings.Contains(again.stdout, "0 careers pages read") {
		t.Fatalf("a page already looked at was read again: %+v", again)
	}
	var page wire.LeadPage
	listed := te.discovery(srv, "leads", "list", "--json").stdout
	if err := json.Unmarshal([]byte(listed), &page); err != nil || len(page.Rows) != 1 || page.Rows[0].Expanded == "" || page.Total != 1 {
		t.Fatalf("json list: %v %+v", err, page)
	}
	assertAPIShape(t, listed)
	if o := te.discovery(srv, "leads", "dismiss", "1", "--reason", "not for me"); o.code != 0 {
		t.Fatalf("dismiss: %+v", o)
	}
	if o := te.discovery(srv, "leads", "list", "--state", "new"); strings.Contains(o.stdout, "Example Tracked") {
		t.Fatalf("a dismissed lead is still new: %+v", o)
	}
	// --sort confidence puts the surest lead first, whatever its age.
	te.withStore(func(st *store.Store) {
		for i, c := range []float64{0.9, 0.2} {
			l := discovery.Lead{Source: "signals", Company: []string{"Sure Corp", "Unsure Corp"}[i], Category: discovery.LeadNoATS, Confidence: c, Evidence: discovery.Evidence{URL: "https://news.example.com/s" + strconv.Itoa(i), Text: "Hiring."}}
			if _, err := discovery.NewRepo(st, nil).LeadAdd(context.Background(), l); err != nil {
				t.Fatal(err)
			}
		}
	})
	surest := func(args ...string) bool {
		o := te.discovery(srv, append([]string{"leads", "list", "--state", "new"}, args...)...)
		return o.code == 0 && strings.Index(o.stdout, "Sure Corp") < strings.Index(o.stdout, "Unsure Corp")
	}
	if surest() || !surest("--sort", "confidence") || surest("--sort", "newest") {
		t.Fatal("--sort confidence lists the surest lead first; newest is the default")
	}
	if o := te.discovery(srv, "leads", "list", "--sort", "score"); o.code != 2 || !strings.Contains(o.stderr, "--sort must be newest or confidence") {
		t.Fatalf("unknown sort: %+v", o)
	}
	if o := te.discovery(srv, "leads", "bogus"); o.code != 2 {
		t.Fatalf("unknown subcommand: %+v", o)
	}
}

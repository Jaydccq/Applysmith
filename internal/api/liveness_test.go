package api

import (
	"context"
	"testing"

	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

type pageReaderFunc func(ctx context.Context, u string) (discovery.PageRead, error)

func (f pageReaderFunc) Read(ctx context.Context, u string) (discovery.PageRead, error) {
	return f(ctx, u)
}

func TestMirror_DiscoveryAndApplyShareOneResolver(t *testing.T) {
	sources := &apply.MirrorSources{
		Page: func(context.Context, string) (string, int) { return "", 404 },
	}
	wrapped := scanMirror{sources}
	const u = "https://www.ziprecruiter.com/c/Example-Co/Job/Backend-Engineer/-in-Austin,TX"
	got := wrapped.Resolve(context.Background(), u, "Example Co", "Backend Engineer")
	want := sources.Resolve(context.Background(), u, apply.MirrorHint{Company: "Example Co", Role: "Backend Engineer"})
	if got.URL != want.URL || got.Reason != want.Reason {
		t.Fatalf("discovery got %+v, apply got %+v", got, want)
	}
	if !wrapped.IsMirror(u) || wrapped.IsMirror("https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001") {
		t.Fatal("the mirror test is not the apply chain's")
	}
	h := newDomainHarness(t)
	sc := h.srv.newScanner(h.domainOf(), workspace.Profile{}, nil)
	if _, ok := sc.Mirror.(scanMirror); !ok {
		t.Fatalf("a scan resolves mirrors through %T", sc.Mirror)
	}
}

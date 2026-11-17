package api

import (
	"context"

	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/vault"
)

// scanMirror gives a scan the apply chain's mirror resolver, so a scan and a
// later apply run call one implementation.
type scanMirror struct{ m *apply.MirrorSources }

func (scanMirror) IsMirror(raw string) bool { return apply.IsAggregatorURL(raw) }

func (s scanMirror) Resolve(ctx context.Context, mirrorURL, company, role string) discovery.MirrorResult {
	r := s.m.Resolve(ctx, mirrorURL, apply.MirrorHint{Company: company, Role: role})
	return discovery.MirrorResult{URL: r.URL, Reason: r.Reason}
}

func (scanMirror) AggregatorHosts() []string { return apply.AggregatorHosts() }

// VaultSecret reads a secret for a keyed source: the value, or "" when there
// is none or the vault cannot answer.
func VaultSecret(v *vault.Vault) func(string) string {
	if v == nil {
		return nil
	}
	return func(name string) string {
		val, err := v.Get(name)
		if err != nil {
			return ""
		}
		return val
	}
}

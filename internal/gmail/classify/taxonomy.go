package classify

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"applysmith/internal/lazyregexp"
)

//go:embed senders.yml
var defaultSenders []byte

// rawTaxonomy is the YAML shape of both the shipped defaults and the user
// override.
type rawTaxonomy struct {
	TrustedATS         []string            `yaml:"trusted_ats"`
	MultiTenantATS     []string            `yaml:"multi_tenant_ats"`
	VendorATS          []string            `yaml:"vendor_ats"`
	SchedulingTools    []string            `yaml:"scheduling_tools"`
	Webmail            []string            `yaml:"webmail"`
	RestrictedSenders  map[string][]string `yaml:"restricted_senders"`
	TenantOverrides    map[string]string   `yaml:"tenant_overrides"`
	GenericDisplay     []string            `yaml:"generic_display_names"`
	OpaqueDisplay      []string            `yaml:"opaque_display_names"`
	GenericLocalParts  []string            `yaml:"generic_local_parts"`
	TenantSlugSuffixes []string            `yaml:"tenant_slug_suffixes"`
	JobBoardBrands     []string            `yaml:"job_board_brands"`
}

// Taxonomy is the merged sender knowledge the classifier reads.
type Taxonomy struct {
	TrustedATS, MultiTenantATS, VendorATS, SchedulingTools, Webmail  map[string]bool
	GenericDisplay, OpaqueDisplay, GenericLocalParts, JobBoardBrands map[string]bool
	// Restricted maps a domain to the only signal types it may produce.
	Restricted         map[string]map[string]bool
	TenantOverrides    map[string]string
	TenantSlugSuffixes []string

	artOnce sync.Once
	art     *lazyregexp.Regexp
}

func parseRaw(data []byte) (rawTaxonomy, error) {
	var raw rawTaxonomy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return rawTaxonomy{}, err
	}
	return raw, nil
}

func mergeLists(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		k := strings.ToLower(strings.TrimSpace(s))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func merge(base, over rawTaxonomy) rawTaxonomy {
	out := base
	out.TrustedATS = mergeLists(base.TrustedATS, over.TrustedATS)
	out.MultiTenantATS = mergeLists(base.MultiTenantATS, over.MultiTenantATS)
	out.VendorATS = mergeLists(base.VendorATS, over.VendorATS)
	out.SchedulingTools = mergeLists(base.SchedulingTools, over.SchedulingTools)
	out.Webmail = mergeLists(base.Webmail, over.Webmail)
	out.GenericDisplay = mergeLists(base.GenericDisplay, over.GenericDisplay)
	out.OpaqueDisplay = mergeLists(base.OpaqueDisplay, over.OpaqueDisplay)
	out.GenericLocalParts = mergeLists(base.GenericLocalParts, over.GenericLocalParts)
	out.TenantSlugSuffixes = mergeLists(base.TenantSlugSuffixes, over.TenantSlugSuffixes)
	out.JobBoardBrands = mergeLists(base.JobBoardBrands, over.JobBoardBrands)
	out.RestrictedSenders = map[string][]string{}
	for k, v := range base.RestrictedSenders {
		out.RestrictedSenders[k] = v
	}
	for k, v := range over.RestrictedSenders {
		out.RestrictedSenders[k] = v
	}
	out.TenantOverrides = map[string]string{}
	for k, v := range base.TenantOverrides {
		out.TenantOverrides[k] = v
	}
	for k, v := range over.TenantOverrides {
		out.TenantOverrides[k] = v
	}
	return out
}

func set(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		if k := strings.ToLower(strings.TrimSpace(s)); k != "" {
			m[k] = true
		}
	}
	return m
}

func build(raw rawTaxonomy) *Taxonomy {
	t := &Taxonomy{
		TrustedATS: set(raw.TrustedATS), MultiTenantATS: set(raw.MultiTenantATS), VendorATS: set(raw.VendorATS),
		SchedulingTools: set(raw.SchedulingTools), Webmail: set(raw.Webmail),
		GenericDisplay: set(raw.GenericDisplay), OpaqueDisplay: set(raw.OpaqueDisplay),
		GenericLocalParts: set(raw.GenericLocalParts), JobBoardBrands: set(raw.JobBoardBrands),
		Restricted: map[string]map[string]bool{}, TenantOverrides: map[string]string{},
		TenantSlugSuffixes: mergeLists(raw.TenantSlugSuffixes, nil),
	}
	for d, types := range raw.RestrictedSenders {
		t.Restricted[strings.ToLower(strings.TrimSpace(d))] = set(types)
	}
	for k, v := range raw.TenantOverrides {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" && strings.TrimSpace(v) != "" {
			t.TenantOverrides[k] = strings.TrimSpace(v)
		}
	}
	return t
}

// Defaults returns the shipped taxonomy.
func Defaults() *Taxonomy {
	raw, err := parseRaw(defaultSenders)
	if err != nil {
		panic(fmt.Sprintf("classify: shipped taxonomy is invalid: %v", err))
	}
	return build(raw)
}

// WithOverride merges a user override over the defaults. A malformed override
// is ignored: the defaults are returned together with the error, so sync keeps
// running and the doctor can report the file.
func WithOverride(override []byte) (*Taxonomy, error) {
	base, err := parseRaw(defaultSenders)
	if err != nil {
		panic(fmt.Sprintf("classify: shipped taxonomy is invalid: %v", err))
	}
	over, err := parseRaw(override)
	if err != nil {
		return build(base), fmt.Errorf("gmail-senders.yml is not valid: %w", err)
	}
	return build(merge(base, over)), nil
}

// domainIn reports whether domain equals or is a subdomain of a listed host.
func domainIn(domain string, hosts map[string]bool) bool {
	for d := domain; d != ""; {
		if hosts[d] {
			return true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return false
		}
		d = d[i+1:]
	}
	return false
}

// restrictedFor returns the allowed types of a restricted sender domain.
func (t *Taxonomy) restrictedFor(domain string) (map[string]bool, bool) {
	for d := domain; d != ""; {
		if allowed, ok := t.Restricted[d]; ok {
			return allowed, true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return nil, false
}

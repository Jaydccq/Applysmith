package workspace

import (
	"gopkg.in/yaml.v3"
)

// Policy is policy.yml. Absent keys take DefaultPolicy; a missing or invalid
// file takes RestrictivePolicy.
type Policy struct {
	Version       int           `yaml:"version" v:"required,enum=1"`
	Cooldowns     Cooldowns     `yaml:"cooldowns"`
	CaptchaSolver string        `yaml:"captcha_solver" v:"nonnull,enum=on|off"`
	RemoteAccess  RemoteAccess  `yaml:"remote_access"`
	Tracker       TrackerPolicy `yaml:"tracker"`
}

// Cooldowns are hours per risk signal; 0 disables the cooldown.
type Cooldowns struct {
	Captcha              int `yaml:"captcha" v:"nonnull,min=0,max=2160"`
	RateLimited          int `yaml:"rate_limited" v:"nonnull,min=0,max=2160"`
	AccountLocked        int `yaml:"account_locked" v:"nonnull,min=0,max=2160"`
	VerificationRequired int `yaml:"verification_required" v:"nonnull,min=0,max=2160"`
	LoginRedirect        int `yaml:"login_redirect" v:"nonnull,min=0,max=2160"`
	SilentDegradation    int `yaml:"silent_degradation" v:"nonnull,min=0,max=2160"`
}

type RemoteAccess struct {
	Mode             string `yaml:"mode" v:"nonnull,enum=off|tunnel"`
	TunnelPort       int    `yaml:"tunnel_port" v:"nonnull,min=1024,max=65535"`
	AllowScreenshots bool   `yaml:"allow_screenshots" v:"nonnull"`
	Tunnel           Tunnel `yaml:"tunnel"`
}

type Tunnel struct {
	Binary    string `yaml:"binary" v:"nonnull,nonblank"`
	PublicURL string `yaml:"public_url" v:"format=https"`
}

type TrackerPolicy struct {
	Followup Followup `yaml:"followup"`
}

type Followup struct {
	FirstAfterDays  int `yaml:"first_after_days" v:"nonnull,min=1"`
	RepeatEveryDays int `yaml:"repeat_every_days" v:"nonnull,min=1"`
	MaxFollowups    int `yaml:"max_followups" v:"nonnull,min=0"`
	CloseAfterDays  int `yaml:"close_after_days" v:"nonnull,min=1"`
}

// DefaultPolicy returns the values a policy.yml takes for absent keys.
func DefaultPolicy() Policy {
	return Policy{
		Version:       1,
		CaptchaSolver: "on",
		Cooldowns: Cooldowns{
			Captcha: 168, RateLimited: 168, AccountLocked: 720,
			VerificationRequired: 72, LoginRedirect: 168, SilentDegradation: 24,
		},
		RemoteAccess: RemoteAccess{Mode: "off", TunnelPort: 47321, Tunnel: Tunnel{Binary: "auto"}},
		Tracker:      TrackerPolicy{Followup: Followup{FirstAfterDays: 8, RepeatEveryDays: 7, MaxFollowups: 2, CloseAfterDays: 35}},
	}
}

// RestrictivePolicy is what the system applies while policy.yml is missing,
// unreadable or invalid: no remote access and no captcha solver.
func RestrictivePolicy() Policy {
	p := DefaultPolicy()
	p.CaptchaSolver = "off"
	p.RemoteAccess.Mode = "off"
	return p
}

// ParsePolicy validates src as policy.yml and decodes it over the defaults.
// On any issue the returned policy is the restrictive one.
func ParsePolicy(src []byte) (Policy, Report) {
	rep, root := validateDoc[Policy](FilePolicy.Name(), src)
	if !rep.OK() {
		return RestrictivePolicy(), rep
	}
	p := DefaultPolicy()
	decodeDoc[Policy](root, &p)
	return p, rep
}

// AnswerBankEnvelope is the top level of answer-bank.yml. The answer-bank
// capability owns the content of entries, employers and overrides.
type AnswerBankEnvelope struct {
	Version   int         `yaml:"version" v:"required,enum=1"`
	Employers yaml.Node   `yaml:"employers"`
	Entries   []yaml.Node `yaml:"entries"`
	Overrides yaml.Node   `yaml:"overrides"`
}

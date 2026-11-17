package remote

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var bg = context.Background()

type rig struct {
	d     *Devices
	db    *store.Store
	clock *clock
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(bg, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t: time.Date(2027, 3, 3, 8, 0, 0, 0, time.UTC)}
	d, err := Open(bg, db, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{d: d, db: db, clock: c}
}

func (r *rig) pair(t *testing.T, label string) (Device, string) {
	t.Helper()
	o, err := r.d.CreateOffer()
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := r.d.Claim(bg, o.Code, label)
	if err != nil {
		t.Fatal(err)
	}
	return dev, token
}

func TestRemote_CodesAreNotInterchangeable(t *testing.T) {
	r := newRig(t)
	// An extension pairing code has ten characters of another alphabet.
	if _, _, err := r.d.Claim(bg, "ABCDEF2345", "Phone"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("an extension-shaped code: %v", err)
	}
	if _, _, err := r.d.Claim(bg, "", "Phone"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("an empty code: %v", err)
	}
}

func TestRemote_TokenStoredHashed(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Alex's phone")
	if len(token) != 43 {
		t.Errorf("a 32-byte token is 43 base64url characters, got %d", len(token))
	}
	var sum, network string
	err := r.db.Read().QueryRow(`SELECT token_sha256, last_network FROM device_sessions WHERE id = ?`, dev.ID).Scan(&sum, &network)
	if err != nil {
		t.Fatal(err)
	}
	if sum == token || strings.Contains(sum, token) || len(sum) != 64 || network != "tunnel" {
		t.Errorf("stored digest %q network %q", sum, network)
	}
	var n int
	_ = r.db.Read().QueryRow(`SELECT count(*) FROM device_sessions WHERE token_sha256 = ? OR label = ?`, token, token).Scan(&n)
	if n != 0 {
		t.Error("the token itself is in the database")
	}
}

func TestRemote_LabelRules(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{{"  Phone  ", true}, {"   ", false}, {strings.Repeat("x", 41), false}, {"line\nbreak", false}} {
		got, err := ValidLabel(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.in, err)
		}
		if tc.ok && got != strings.TrimSpace(tc.in) {
			t.Errorf("%q trimmed to %q", tc.in, got)
		}
	}
}

func TestRemote_AuthenticateAndIdleExpiry(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Phone")
	if got, ok := r.d.Authenticate(bg, token); !ok || got.ID != dev.ID {
		t.Fatalf("authenticate: %v %v", got, ok)
	}
	if _, ok := r.d.Authenticate(bg, token+"x"); ok {
		t.Error("a wrong token passed")
	}
	r.clock.advance(29*24*time.Hour + 23*time.Hour)
	if _, ok := r.d.Authenticate(bg, token); !ok {
		t.Fatal("a token idle for less than 30 days was refused")
	}
	// The request above counts as use: another 30 days from it.
	r.clock.advance(30*24*time.Hour + time.Second)
	if _, ok := r.d.Authenticate(bg, token); ok {
		t.Error("a token idle for more than 30 days was accepted")
	}
	if n := len(r.d.List()); n != 0 {
		t.Errorf("an expired device is still listed: %d", n)
	}
}

func TestRemote_IdleExpiryAfterRestart(t *testing.T) {
	r := newRig(t)
	_, token := r.pair(t, "Phone")
	r.clock.advance(31 * 24 * time.Hour)
	again, err := Open(bg, r.db, r.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Authenticate(bg, token); ok {
		t.Error("a token idle for 31 days was accepted after a restart")
	}
}

func TestRemote_DeviceSurvivesARestartAndLastSeenIsPersisted(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Phone")
	r.clock.advance(time.Minute)
	if _, ok := r.d.Authenticate(bg, token); !ok {
		t.Fatal("not accepted")
	}
	again, _ := Open(bg, r.db, r.clock.now)
	got, ok := again.Authenticate(bg, token)
	if !ok || got.ID != dev.ID {
		t.Fatalf("after a restart: %v %v", got, ok)
	}
	var seen string
	_ = r.db.Read().QueryRow(`SELECT last_seen_at FROM device_sessions WHERE id = ?`, dev.ID).Scan(&seen)
	if parseStamp(seen).Before(r.clock.now().Add(-time.Second)) {
		t.Errorf("last seen %s was not persisted", seen)
	}
}

func TestRemote_DeviceLimit(t *testing.T) {
	r := newRig(t)
	for i := range MaxDevices {
		r.pair(t, "Phone "+string(rune('A'+i)))
	}
	o, _ := r.d.CreateOffer()
	if _, _, err := r.d.Claim(bg, o.Code, "Eleventh"); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("eleventh device: %v", err)
	}
	devs := r.d.List()
	if ok, err := r.d.Revoke(bg, devs[0].ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, _, err := r.d.Claim(bg, o.Code, "Eleventh"); err != nil {
		t.Errorf("after a revoke, the offer that was refused still works: %v", err)
	}
}

func TestRemote_RevokeIsImmediateAndPermanent(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Phone")
	other, otherToken := r.pair(t, "Tablet")
	if ok, err := r.d.Revoke(bg, dev.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok := r.d.Authenticate(bg, token); ok {
		t.Error("a revoked token was accepted")
	}
	if again, _ := r.d.Revoke(bg, dev.ID); again {
		t.Error("revoking twice reported a device")
	}
	if got, ok := r.d.Authenticate(bg, otherToken); !ok || got.ID != other.ID {
		t.Error("revoking one device ended another")
	}
	restarted, _ := Open(bg, r.db, r.clock.now)
	if _, ok := restarted.Authenticate(bg, token); ok {
		t.Error("a revoked token became valid after a restart")
	}
	ids, err := r.d.RevokeAll(bg)
	if err != nil || len(ids) != 1 || ids[0] != other.ID {
		t.Errorf("revoke all: %v %v", ids, err)
	}
	if _, ok := r.d.Authenticate(bg, otherToken); ok {
		t.Error("revoke-all left a token valid")
	}
}

// Revoking every device also ends the pairing offers that were waiting, so a
// link made before the revoke pairs nothing after it, with or without a device.
func TestRemote_RevokeAllVoidsOutstandingOffers(t *testing.T) {
	r := newRig(t)
	for _, paired := range []bool{false, true} {
		if paired {
			r.pair(t, "Phone")
		}
		o, err := r.d.CreateOffer()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.d.RevokeAll(bg); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.d.Claim(bg, o.Code, "Tablet"); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("with a device paired %v: an offer from before revoke-all was claimed: %v", paired, err)
		}
	}
}

func TestRemote_ConnectedFlag(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Phone")
	r.clock.advance(30 * time.Second)
	got, _ := r.d.Authenticate(bg, token)
	r.clock.advance(30 * time.Second)
	if !r.d.Connected(got) {
		t.Error("a request 30 seconds ago is not connected")
	}
	r.clock.advance(31 * time.Second)
	if listed := r.d.List(); r.d.Connected(listed[0]) {
		t.Errorf("a device quiet for 61 seconds is connected: %+v", dev)
	}
}

func TestRemote_EventsRecordedWithoutSecretsRetainedNinetyDays(t *testing.T) {
	r := newRig(t)
	dev, token := r.pair(t, "Phone")
	o, _ := r.d.CreateOffer()
	_ = r.d.Record(bg, EventPairClaim, dev.ID, "203.0.113.7", "paired Phone")
	_ = r.d.Record(bg, EventPairFailed, "", "203.0.113.8", "wrong code")
	r.clock.advance(91 * 24 * time.Hour)
	_ = r.d.Record(bg, EventRevoke, dev.ID, "dashboard", "revoked Phone")
	if err := r.d.Sweep(bg); err != nil {
		t.Fatal(err)
	}
	evs, err := r.d.Events(bg, 100)
	if err != nil || len(evs) != 1 || evs[0].Kind != EventRevoke {
		t.Fatalf("after the sweep: %+v %v", evs, err)
	}
	var dump string
	rows, _ := r.db.Read().Query(`SELECT kind || device_id || source || detail FROM remote_events`)
	for rows.Next() {
		var s sql.NullString
		_ = rows.Scan(&s)
		dump += s.String
	}
	if strings.Contains(dump, token) || strings.Contains(dump, o.Code) {
		t.Error("an event holds a secret")
	}
	if err := r.d.Record(bg, "something_else", "", "", ""); err == nil {
		t.Error("an unknown event kind was stored")
	}
}

func TestRemote_SettingsFromPolicy(t *testing.T) {
	dir := t.TempDir()
	ws := workspace.Open(dir)
	if got := ReadSettings(ws); got.Mode != ModeOff || got.TunnelPort != 47321 || got.Binary != "auto" {
		t.Errorf("no policy file: %+v", got)
	}
	write := func(body string) {
		t.Helper()
		if err := writePolicy(dir, body); err != nil {
			t.Fatal(err)
		}
	}
	write("version: 1\nremote_access:\n  mode: tunnel\n  tunnel_port: 50000\n  allow_screenshots: true\n")
	if got := ReadSettings(ws); got.Mode != ModeTunnel || got.TunnelPort != 50000 || !got.AllowScreenshots {
		t.Errorf("tunnel: %+v", got)
	}
	for _, body := range []string{"version: 1\nremote_access:\n  mode: lan\n", "version: 1\nremote_access:\n  mode: wan\n", "version: [", "version: 1\nremote_access:\n  mode: tunnel\n  tunnel_port: 80\n"} {
		write(body)
		if got := ReadSettings(ws); got.Mode != ModeOff {
			t.Errorf("%q must read as off, got %+v", body, got)
		}
	}
}

func TestRemote_OfferExpiresAt300sSingleUseMaxThree(t *testing.T) {
	r := newRig(t)
	first, _ := r.d.CreateOffer()
	r.clock.advance(299 * time.Second)
	if _, _, err := r.d.Claim(bg, first.Code, "Phone"); err != nil {
		t.Errorf("an offer 299 seconds old: %v", err)
	}
	if _, _, err := r.d.Claim(bg, first.Code, "Tablet"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("an offer claimed a second time: %v", err)
	}
	second, _ := r.d.CreateOffer()
	r.clock.advance(301 * time.Second)
	if _, _, err := r.d.Claim(bg, second.Code, "Phone"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("an offer 301 seconds old: %v", err)
	}
	var codes []string
	for range 4 {
		o, _ := r.d.CreateOffer()
		codes = append(codes, o.Code)
	}
	if _, _, err := r.d.Claim(bg, codes[0], "Phone"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("the fourth offer left the first usable: %v", err)
	}
	for _, c := range codes[1:] {
		if _, _, err := r.d.Claim(bg, c, "Phone"); err != nil {
			t.Errorf("one of the three newest offers: %v", err)
		}
	}
}

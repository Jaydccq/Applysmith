package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/store"
)

// Limits from the specification.
const (
	OfferTTL        = 300 * time.Second
	MaxOffers       = 3
	MaxDevices      = 10
	IdleExpiry      = 30 * 24 * time.Hour
	ConnectedWindow = 60 * time.Second
	MaxLabel        = 40
	// Network is the only network a device is ever seen on.
	Network = "tunnel"
	// persistEvery bounds how often a request writes the last-seen time.
	persistEvery = 30 * time.Second
)

var (
	// ErrInvalidCode means the pairing code is unknown, used or expired.
	ErrInvalidCode = errors.New("remote: the pairing code is not valid")
	// ErrDeviceLimit means 10 devices are already active.
	ErrDeviceLimit = errors.New("remote: 10 devices are already paired; revoke one first")
	// ErrLabel means the device label is not 1 to 40 printable characters.
	ErrLabel = errors.New("remote: a device label has 1 to 40 printable characters")
)

// Device is a paired device.
type Device struct {
	ID        string
	Label     string
	CreatedAt time.Time
	LastSeen  time.Time
	Network   string
}

// Offer is a one-time pairing code.
type Offer struct {
	Code      string
	ExpiresAt time.Time
}

type liveDevice struct {
	Device
	sum       [sha256.Size]byte
	persisted time.Time
}

type liveOffer struct {
	sum     [sha256.Size]byte
	expires time.Time
}

// Devices keeps the active devices in memory, as digests, and the database as
// the record. Only this process writes the table, so the cache stays exact.
type Devices struct {
	db  *store.Store
	now func() time.Time

	mu      sync.Mutex
	devices []*liveDevice
	offers  []liveOffer
}

const timeLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(v string) time.Time {
	t, _ := time.Parse(timeLayout, v)
	return t
}

// Open loads the active devices.
func Open(ctx context.Context, db *store.Store, now func() time.Time) (*Devices, error) {
	d := &Devices{db: db, now: now}
	rows, err := db.Read().QueryContext(ctx, `SELECT id, label, token_sha256, created_at, last_seen_at FROM device_sessions WHERE revoked_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, label, sum, created string
		var seen sql.NullString
		if err := rows.Scan(&id, &label, &sum, &created, &seen); err != nil {
			return nil, err
		}
		raw, err := hex.DecodeString(sum)
		if err != nil || len(raw) != sha256.Size {
			continue
		}
		ld := &liveDevice{Device: Device{ID: id, Label: label, CreatedAt: parseStamp(created), Network: Network}}
		ld.LastSeen = ld.CreatedAt
		if seen.Valid {
			ld.LastSeen = parseStamp(seen.String)
		}
		ld.persisted = ld.LastSeen
		copy(ld.sum[:], raw)
		if !d.idleLocked(ld) {
			d.devices = append(d.devices, ld)
		}
	}
	return d, rows.Err()
}

func (d *Devices) idleLocked(ld *liveDevice) bool { return d.now().Sub(ld.LastSeen) > IdleExpiry }

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func digest(s string) [sha256.Size]byte { return sha256.Sum256([]byte(s)) }

// CreateOffer makes a one-time code of 128 random bits. A fourth outstanding
// offer invalidates the oldest.
func (d *Devices) CreateOffer() (Offer, error) {
	code, err := randomToken(16)
	if err != nil {
		return Offer{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropExpiredOffersLocked()
	if len(d.offers) >= MaxOffers {
		d.offers = d.offers[len(d.offers)-MaxOffers+1:]
	}
	exp := d.now().Add(OfferTTL)
	d.offers = append(d.offers, liveOffer{sum: digest(code), expires: exp})
	return Offer{Code: code, ExpiresAt: exp}, nil
}

// VoidOffers ends every outstanding offer: a code made before it pairs nothing.
func (d *Devices) VoidOffers() {
	d.mu.Lock()
	d.offers = nil
	d.mu.Unlock()
}

func (d *Devices) dropExpiredOffersLocked() {
	kept := d.offers[:0]
	for _, o := range d.offers {
		if o.expires.After(d.now()) {
			kept = append(kept, o)
		}
	}
	d.offers = kept
}

// ValidLabel trims a label and checks it: 1 to 40 printable characters.
func ValidLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	n := utf8.RuneCountInString(label)
	if n < 1 || n > MaxLabel {
		return "", ErrLabel
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			return "", ErrLabel
		}
	}
	return label, nil
}

// Claim spends an offer and issues a device token, returned once. A claim past
// the device limit leaves the offer alone.
func (d *Devices) Claim(ctx context.Context, code, label string) (Device, string, error) {
	label, err := ValidLabel(label)
	if err != nil {
		return Device{}, "", err
	}
	sum := digest(code)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropExpiredOffersLocked()
	found := -1
	for i, o := range d.offers {
		if subtle.ConstantTimeCompare(sum[:], o.sum[:]) == 1 {
			found = i
		}
	}
	if found < 0 {
		return Device{}, "", ErrInvalidCode
	}
	d.dropIdleLocked()
	if len(d.devices) >= MaxDevices {
		return Device{}, "", ErrDeviceLimit
	}
	token, err := randomToken(32)
	if err != nil {
		return Device{}, "", err
	}
	id, err := randomToken(8)
	if err != nil {
		return Device{}, "", err
	}
	now := d.now()
	ld := &liveDevice{Device: Device{ID: "dev_" + id, Label: label, CreatedAt: now, LastSeen: now, Network: Network}, sum: digest(token), persisted: now}
	err = d.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO device_sessions (id, label, token_sha256, last_network, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ld.ID, ld.Label, hex.EncodeToString(ld.sum[:]), Network, stamp(now), stamp(now))
		return err
	})
	if err != nil {
		return Device{}, "", err
	}
	d.offers = append(d.offers[:found:found], d.offers[found+1:]...)
	d.devices = append(d.devices, ld)
	return ld.Device, token, nil
}

func (d *Devices) dropIdleLocked() {
	kept := d.devices[:0]
	for _, ld := range d.devices {
		if !d.idleLocked(ld) {
			kept = append(kept, ld)
		}
	}
	d.devices = kept
}

// Authenticate returns the device a token belongs to and records the request
// as its last use. Every active device is compared, in constant time.
func (d *Devices) Authenticate(ctx context.Context, token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	sum := digest(token)
	d.mu.Lock()
	d.dropIdleLocked()
	var hit *liveDevice
	for _, ld := range d.devices {
		if subtle.ConstantTimeCompare(sum[:], ld.sum[:]) == 1 {
			hit = ld
		}
	}
	if hit == nil {
		d.mu.Unlock()
		return Device{}, false
	}
	now := d.now()
	hit.LastSeen = now
	dev, persist := hit.Device, now.Sub(hit.persisted) >= persistEvery
	if persist {
		hit.persisted = now
	}
	d.mu.Unlock()
	if persist {
		_ = d.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE device_sessions SET last_seen_at = ? WHERE id = ?`, stamp(now), dev.ID)
			return err
		})
	}
	return dev, true
}

// List returns the active devices, oldest first.
func (d *Devices) List() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropIdleLocked()
	out := make([]Device, len(d.devices))
	for i, ld := range d.devices {
		out[i] = ld.Device
	}
	return out
}

// Active reports whether the device is still paired: not revoked and not idle
// for 30 days.
func (d *Devices) Active(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropIdleLocked()
	for _, ld := range d.devices {
		if ld.ID == id {
			return true
		}
	}
	return false
}

// Count is the number of active devices.
func (d *Devices) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropIdleLocked()
	return len(d.devices)
}

// Connected reports a request within the connected window; an open stream is
// the caller's to add.
func (d *Devices) Connected(dev Device) bool { return d.now().Sub(dev.LastSeen) <= ConnectedWindow }

// Revoke ends one device's token for good. It reports whether the device was
// active.
func (d *Devices) Revoke(ctx context.Context, id string) (bool, error) {
	n, err := d.revoke(ctx, func(ld *liveDevice) bool { return ld.ID == id })
	return n > 0, err
}

// RevokeAll ends every device's token and returns their identifiers. The
// outstanding offers end first, so nothing made before it can pair a device
// after it.
func (d *Devices) RevokeAll(ctx context.Context) ([]string, error) {
	d.VoidOffers()
	var ids []string
	_, err := d.revoke(ctx, func(ld *liveDevice) bool { ids = append(ids, ld.ID); return true })
	return ids, err
}

func (d *Devices) revoke(ctx context.Context, match func(*liveDevice) bool) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var hit, kept []*liveDevice
	for _, ld := range d.devices {
		if match(ld) {
			hit = append(hit, ld)
		} else {
			kept = append(kept, ld)
		}
	}
	if len(hit) == 0 {
		return 0, nil
	}
	now := stamp(d.now())
	err := d.db.Write(ctx, func(tx *sql.Tx) error {
		for _, ld := range hit {
			if _, err := tx.ExecContext(ctx, `UPDATE device_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, ld.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	d.devices = kept
	return len(hit), nil
}

// OwnSession records that a device started a conversation. A remote device may
// post to and read only the conversations it started.
func (d *Devices) OwnSession(ctx context.Context, sessionID, deviceID string) error {
	return d.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO remote_sessions (session_id, device_id) VALUES (?, ?)`, sessionID, deviceID)
		return err
	})
}

// OwnsSession reports whether the device started the conversation.
func (d *Devices) OwnsSession(ctx context.Context, sessionID, deviceID string) bool {
	var n int
	err := d.db.Read().QueryRowContext(ctx, `SELECT count(*) FROM remote_sessions WHERE session_id = ? AND device_id = ?`, sessionID, deviceID).Scan(&n)
	return err == nil && n > 0
}

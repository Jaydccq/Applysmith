// Package browserimport reads a user's own sign-in cookies from an installed
// Chromium-family browser on this computer, so they can be brought into the
// managed apply profile without the Applysmith extension. It is the local
// counterpart of the extension's session sync: the extension pushes cookies
// from a running Chrome, this reads them straight from the browser's own
// cookie store.
//
// Only macOS is supported. A Chromium browser on macOS encrypts each cookie
// value with a key derived from a password it keeps in the login keychain
// ("<Browser> Safe Storage"); reading that password prompts the user, so no
// value is ever decrypted without the user's own keychain approval. Listing
// the sites a browser holds reads only host names and counts and needs no
// password.
//
// Values are secrets: this package returns them only as wire.SessionCookie for
// the import path, and never logs or formats them.
package browserimport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"crypto/pbkdf2"
	"github.com/zalando/go-keyring"

	_ "modernc.org/sqlite" // pure-Go driver, as the store uses

	"applysmith/internal/api/wire"
)

// Errors a caller maps to its own envelope.
var (
	// ErrUnsupportedOS is returned off macOS: other platforms lock their cookie
	// stores against other processes (Windows DPAPI, Linux Secret Service), so
	// the extension's sync is the path there.
	ErrUnsupportedOS = errors.New("importing cookies from an installed browser is supported on macOS only")
	// ErrNoKey is the browser's Safe Storage key not being in the keychain:
	// the browser has not run, or stores no key.
	ErrNoKey = errors.New("this browser has no saved Safe Storage key; sign in to it once and try again")
	// ErrKeyLocked is the keychain refusing the key: the user declined, or the
	// keychain is locked.
	ErrKeyLocked = errors.New("the browser's cookies are still locked; enter your Mac login password when macOS asks and choose Allow")
)

// chromium is one known Chromium-family browser: where it keeps its profiles
// under the user's Application Support, and the keychain entry holding its
// Safe Storage password.
type chromium struct {
	id, label string
	dir       string // relative to ~/Library/Application Support
	service   string // keychain service
	account   string // keychain account
}

// chromiums are the browsers browserimport knows. Each encrypts cookies the
// same way; only the profile directory and the keychain entry differ.
var chromiums = []chromium{
	{"chrome", "Google Chrome", "Google/Chrome", "Chrome Safe Storage", "Chrome"},
	{"chrome-beta", "Google Chrome Beta", "Google/Chrome Beta", "Chrome Safe Storage", "Chrome"},
	{"edge", "Microsoft Edge", "Microsoft Edge", "Microsoft Edge Safe Storage", "Microsoft Edge"},
	{"brave", "Brave", "BraveSoftware/Brave-Browser", "Brave Safe Storage", "Brave"},
	{"chromium", "Chromium", "Chromium", "Chromium Safe Storage", "Chromium"},
	{"vivaldi", "Vivaldi", "Vivaldi", "Vivaldi Safe Storage", "Vivaldi"},
}

// Source is one profile of one installed browser: the thing a user chooses to
// import from. cookiesPath is the profile's cookie database on disk.
type Source struct {
	BrowserID    string `json:"browserId"`
	BrowserLabel string `json:"browserLabel"`
	// Profile is the browser profile's display name ("Default", "Profile 1").
	Profile string `json:"profile"`
	// ID identifies the source to the import call: "<browserId>/<profileDir>".
	ID string `json:"id"`

	cookiesPath string
	service     string
	account     string
}

// Site is one host a source holds cookies for, with how many. Listing needs no
// keychain.
type Site struct {
	Host    string       `json:"host"`
	Cookies wire.SafeInt `json:"cookies"`
}

// Reader reads installed browsers. KeyFor reads a keychain Safe Storage
// password; nil uses the login keychain. AppSupport overrides the search root;
// empty uses the user's Application Support. Both exist for tests.
type Reader struct {
	KeyFor     func(service, account string) (string, error)
	AppSupport string
}

// appSupport is the root the reader scans.
func (r Reader) appSupport() (string, error) {
	if r.AppSupport != "" {
		return r.AppSupport, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support"), nil
}

// keyFor reads a browser's Safe Storage password from the keychain.
func (r Reader) keyFor(service, account string) (string, error) {
	if r.KeyFor != nil {
		return r.KeyFor(service, account)
	}
	return keyring.Get(service, account)
}

// Sources lists the browser profiles on this computer that hold a cookie
// store. It reads only the filesystem: no keychain, no cookie values. Off
// macOS it returns nothing, since no source can be read there.
func (r Reader) Sources() ([]Source, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	root, err := r.appSupport()
	if err != nil {
		return nil, err
	}
	var out []Source
	for _, b := range chromiums {
		base := filepath.Join(root, b.dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		for _, prof := range profileDirs(base) {
			path := cookieFile(filepath.Join(base, prof))
			if path == "" {
				continue
			}
			out = append(out, Source{
				BrowserID: b.id, BrowserLabel: b.label, Profile: prof,
				ID: b.id + "/" + prof, cookiesPath: path, service: b.service, account: b.account,
			})
		}
	}
	return out, nil
}

// source resolves an import id ("<browserId>/<profileDir>") to a Source,
// checking the cookie store still exists.
func (r Reader) source(id string) (Source, error) {
	srcs, err := r.Sources()
	if err != nil {
		return Source{}, err
	}
	i := slices.IndexFunc(srcs, func(s Source) bool { return s.ID == id })
	if i < 0 {
		return Source{}, fmt.Errorf("no installed browser profile is called %q", id)
	}
	return srcs[i], nil
}

// profileDirs are the profile subdirectories of a browser's data directory.
func profileDirs(base string) []string {
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if n := e.Name(); n == "Default" || strings.HasPrefix(n, "Profile ") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// cookieFile is a profile's cookie database, under Network/Cookies on current
// Chromium and Cookies on older builds, or "" when it has none.
func cookieFile(profile string) string {
	for _, rel := range []string{filepath.Join("Network", "Cookies"), "Cookies"} {
		p := filepath.Join(profile, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// Sites lists the hosts a source holds cookies for, with counts. It copies the
// store and reads host names only; no keychain, no values.
func (r Reader) Sites(ctx context.Context, id string) ([]Site, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrUnsupportedOS
	}
	src, err := r.source(id)
	if err != nil {
		return nil, err
	}
	return sitesOf(ctx, src.cookiesPath)
}

func sitesOf(ctx context.Context, cookiesPath string) ([]Site, error) {
	db, cleanup, err := openCopy(cookiesPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, `SELECT host_key, count(*) FROM cookies GROUP BY host_key`)
	if err != nil {
		return nil, fmt.Errorf("read the cookie store: %w", err)
	}
	defer rows.Close()
	byHost := map[string]int{}
	for rows.Next() {
		var host string
		var n int
		if err := rows.Scan(&host, &n); err != nil {
			return nil, err
		}
		byHost[hostOf(host)] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Site, 0, len(byHost))
	for h, n := range byHost {
		out = append(out, Site{Host: h, Cookies: wire.SafeInt(n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

// Read decrypts the cookies a source holds for the given sites and returns them
// as import cookies. It reads the browser's Safe Storage password from the
// keychain, which prompts the user. Hosts scope the read: a cookie is included
// when its host is one of them or lies below it.
func (r Reader) Read(ctx context.Context, id string, hosts []string) ([]wire.SessionCookie, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrUnsupportedOS
	}
	src, err := r.source(id)
	if err != nil {
		return nil, err
	}
	pw, err := r.keyFor(src.service, src.account)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return nil, ErrNoKey
	case err != nil:
		return nil, ErrKeyLocked
	case pw == "":
		return nil, ErrNoKey
	}
	key, err := pbkdf2.Key(sha1.New, pw, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return nil, err
	}
	want := make([]string, len(hosts))
	for i, h := range hosts {
		want[i] = hostOf(h)
	}
	db, cleanup, err := openCopy(src.cookiesPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, `SELECT host_key, name, encrypted_value, value, path,
		expires_utc, is_secure, is_httponly, samesite, is_persistent FROM cookies`)
	if err != nil {
		return nil, fmt.Errorf("read the cookie store: %w", err)
	}
	defer rows.Close()
	var out []wire.SessionCookie
	for rows.Next() {
		var host, name, path, value string
		var enc []byte
		var expires int64
		var secure, httpOnly, sameSite, persistent int
		if err := rows.Scan(&host, &name, &enc, &value, &path, &expires, &secure, &httpOnly, &sameSite, &persistent); err != nil {
			return nil, err
		}
		if !scopedAny(hostOf(host), want) {
			continue
		}
		val := value
		if len(enc) > 0 {
			val, err = decrypt(key, enc, host)
			if err != nil {
				// One unreadable cookie does not fail the import; a sign-in
				// turns on several and the rest carry it.
				continue
			}
		}
		out = append(out, toWire(host, name, val, path, expires, secure != 0, httpOnly != 0, sameSite, persistent != 0))
	}
	return out, rows.Err()
}

// toWire maps a Chromium cookie row to an import cookie. Chromium stores
// expires_utc in microseconds since the Windows FILETIME epoch and host_key
// with a leading dot for a domain cookie.
func toWire(host, name, value, path string, expiresUTC int64, secure, httpOnly bool, sameSite int, persistent bool) wire.SessionCookie {
	c := wire.SessionCookie{
		Name: name, Value: value, Domain: hostOf(host), Path: path,
		Secure: secure, HTTPOnly: httpOnly, HostOnly: !strings.HasPrefix(host, "."),
		Session: !persistent,
	}
	switch sameSite {
	case 0:
		c.SameSite = "no_restriction"
	case 1:
		c.SameSite = "lax"
	case 2:
		c.SameSite = "strict"
	}
	if persistent && expiresUTC > 0 {
		secs := float64(expiresUTC)/1e6 - 11644473600
		if secs > 0 {
			c.ExpirationDate = &secs
		} else {
			c.Session = true
		}
	}
	return c
}

// decrypt returns the plaintext of a Chromium v10 cookie value. host is the
// cookie's host_key, used to strip the SHA-256 host prefix current Chromium
// prepends to the plaintext.
func decrypt(key, enc []byte, host string) (string, error) {
	if len(enc) < 3 || string(enc[:3]) != "v10" {
		return string(enc), nil // an unencrypted value, stored as is
	}
	ct := enc[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("cookie ciphertext is not a whole number of blocks")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pt, err = unpad(pt)
	if err != nil {
		return "", err
	}
	// Chromium 130+ prepends sha256(host_key) to the value; strip it when it
	// matches, which also confirms the key decrypted the value.
	if h := sha256.Sum256([]byte(host)); len(pt) >= sha256.Size && bytes.Equal(pt[:sha256.Size], h[:]) {
		pt = pt[sha256.Size:]
	}
	return string(pt), nil
}

// unpad removes PKCS#7 padding.
func unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > len(b) || n > aes.BlockSize {
		return nil, errors.New("bad padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("bad padding")
		}
	}
	return b[:len(b)-n], nil
}

// openCopy copies a cookie store and its write-ahead log to a temporary
// directory and opens the copy, so a running browser's open database is never
// touched. A current Chromium keeps the store in WAL mode with recent cookies
// (a fresh sign-in among them) in the -wal file, so the copy is opened
// read-write to let SQLite recover the log into it; the shared-memory index is
// left out and rebuilt from the log. The caller must call cleanup.
func openCopy(cookiesPath string) (*sql.DB, func(), error) {
	dir, err := os.MkdirTemp("", "applysmith-cookies-")
	if err != nil {
		return nil, nil, err
	}
	clean := func() { _ = os.RemoveAll(dir) }
	dst := filepath.Join(dir, "Cookies")
	if err := copyFile(cookiesPath, dst); err != nil {
		clean()
		return nil, nil, fmt.Errorf("copy the cookie store: %w", err)
	}
	// The -wal copy is best-effort: a store not in WAL mode has none.
	_ = copyFile(cookiesPath+"-wal", dst+"-wal")
	db, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		clean()
		return nil, nil, err
	}
	return db, func() { _ = db.Close(); clean() }, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// hostOf is a host_key without its leading dot, lowercased.
func hostOf(host string) string { return strings.TrimPrefix(strings.ToLower(host), ".") }

// scopedAny reports whether host is one of sites or lies below one.
func scopedAny(host string, sites []string) bool {
	return slices.ContainsFunc(sites, func(s string) bool {
		return host == s || strings.HasSuffix(host, "."+s)
	})
}

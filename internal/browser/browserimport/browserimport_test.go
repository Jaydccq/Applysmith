package browserimport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"crypto/pbkdf2"
	"github.com/zalando/go-keyring"

	_ "modernc.org/sqlite"
)

const testPassword = "s3cr3t-safe-storage"

// seal encrypts a value the way Chromium does on macOS: v10 + AES-128-CBC under
// the key derived from password, with an optional sha256(host) prefix as
// current Chromium writes.
func seal(t *testing.T, password, host, value string, prefixHost bool) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte(value)
	if prefixHost {
		h := sha256.Sum256([]byte(host))
		pt = append(h[:], pt...)
	}
	pad := aes.BlockSize - len(pt)%aes.BlockSize
	pt = append(pt, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(ct, pt)
	return append([]byte("v10"), ct...)
}

type row struct {
	host, name, value string
	enc               []byte
	persistent        int
	expires           int64
}

// writeProfile builds a Chrome profile with a Cookies store holding rows, under
// a fake Application Support root, and returns the root.
func writeProfile(t *testing.T, rows []row) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "Google", "Chrome", "Default", "Network")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Cookies")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Current Chromium keeps the store in WAL mode; the reader must copy and
	// open such a store, not only a rollback-journal one.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB,
		path TEXT, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER, is_persistent INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO cookies VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.host, r.name, r.value, r.enc, "/", r.expires, 1, 1, 1, r.persistent); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBrowserImport_SitesListsHostsWithoutTheKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("source listing is macOS only")
	}
	root := writeProfile(t, []row{
		{host: "example.com", name: "a", enc: seal(t, testPassword, "example.com", "x", true), persistent: 1},
		{host: "example.com", name: "b", enc: seal(t, testPassword, "example.com", "y", true), persistent: 1},
		{host: ".other.com", name: "c", enc: seal(t, testPassword, ".other.com", "z", true), persistent: 1},
	})
	r := Reader{AppSupport: root, KeyFor: func(string, string) (string, error) {
		t.Fatal("Sites must not read the keychain")
		return "", nil
	}}
	srcs, err := r.Sources()
	if err != nil || len(srcs) != 1 || srcs[0].ID != "chrome/Default" {
		t.Fatalf("sources = %+v, %v", srcs, err)
	}
	sites, err := r.Sites(context.Background(), "chrome/Default")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0].Host != "example.com" || sites[0].Cookies != 2 || sites[1].Host != "other.com" || sites[1].Cookies != 1 {
		t.Fatalf("sites = %+v", sites)
	}
}

func TestBrowserImport_ReadDecryptsScopedCookiesAndStripsTheHostPrefix(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("import is macOS only")
	}
	root := writeProfile(t, []row{
		{host: ".linkedin.com", name: "li_at", enc: seal(t, testPassword, ".linkedin.com", "SIGNIN-TOKEN", true), persistent: 1, expires: 13400000000000000},
		{host: "www.linkedin.com", name: "legacy", enc: seal(t, testPassword, "www.linkedin.com", "old", false), persistent: 0},
		{host: "example.com", name: "skip", enc: seal(t, testPassword, "example.com", "nope", true), persistent: 1},
	})
	calls := 0
	r := Reader{AppSupport: root, KeyFor: func(service, account string) (string, error) {
		calls++
		if service != "Chrome Safe Storage" || account != "Chrome" {
			t.Fatalf("keychain lookup = %q / %q", service, account)
		}
		return testPassword, nil
	}}
	cookies, err := r.Read(context.Background(), "chrome/Default", []string{"linkedin.com"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("keychain reads = %d, want 1", calls)
	}
	if len(cookies) != 2 {
		t.Fatalf("cookies = %d, want 2 (example.com out of scope)", len(cookies))
	}
	got := map[string]string{}
	for _, c := range cookies {
		got[c.Name] = c.Value
	}
	if got["li_at"] != "SIGNIN-TOKEN" {
		t.Fatalf("host-prefixed value = %q", got["li_at"])
	}
	if got["legacy"] != "old" {
		t.Fatalf("plain value = %q", got["legacy"])
	}
	for _, c := range cookies {
		// ".linkedin.com" is a domain cookie (not host-only) and persistent.
		if c.Name == "li_at" && (c.HostOnly || c.Domain != "linkedin.com" || c.Session) {
			t.Fatalf("li_at mapped wrong: %+v", c)
		}
		if c.Name == "legacy" && !c.Session {
			t.Fatalf("a non-persistent cookie is a session cookie: %+v", c)
		}
	}
}

func TestBrowserImport_ReadWithoutAKeyReports(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("import is macOS only")
	}
	root := writeProfile(t, []row{{host: "example.com", name: "a", enc: seal(t, testPassword, "example.com", "x", true), persistent: 1}})
	r := Reader{AppSupport: root, KeyFor: func(string, string) (string, error) { return "", keyring.ErrNotFound }}
	if _, err := r.Read(context.Background(), "chrome/Default", []string{"example.com"}); err != ErrNoKey {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

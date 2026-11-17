package workspace

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSecretKey_CreatedOnceWithOwnerOnlyMode(t *testing.T) {
	w := Open(t.TempDir())
	a, err := w.SecretKey("test.key")
	if err != nil || len(a) != SecretKeyBytes {
		t.Fatalf("%v %d", err, len(a))
	}
	b, err := Open(w.Root).SecretKey("test.key")
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("a second handle reads the same key: %v", err)
	}
	info, err := os.Stat(filepath.Join(w.StateDir(), "test.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, info)
	}
}

func TestSecretKey_ConcurrentFirstUseAgrees(t *testing.T) {
	w := Open(t.TempDir())
	keys := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], _ = w.SecretKey("race.key")
		}()
	}
	wg.Wait()
	for _, k := range keys {
		if len(k) != SecretKeyBytes || !bytes.Equal(k, keys[0]) {
			t.Fatal("every creator must see one key")
		}
	}
}

func TestSecretKey_RefusesAMalformedFileAndAPathName(t *testing.T) {
	w := Open(t.TempDir())
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.StateDir(), "bad.key"), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SecretKey("bad.key"); err == nil {
		t.Fatal("a file that is not a key must not be replaced")
	}
	if _, err := w.SecretKey("../escape.key"); err == nil {
		t.Fatal("a name with a path is refused")
	}
}

func TestSecretKey_LooseModeIsTightenedToOwnerOnly(t *testing.T) {
	w := Open(t.TempDir())
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{7}, SecretKeyBytes)
	path := filepath.Join(w.StateDir(), "loose.key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(raw)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := w.SecretKey("loose.key")
	if err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("the key is still the key: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode().Perm(), err)
	}
}

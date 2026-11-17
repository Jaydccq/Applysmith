package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SecretKeyBytes is the size of a workspace secret key.
const SecretKeyBytes = 32

// SecretKey returns the random key kept in the state directory under name,
// creating it on first use. The file is mode 0600 and is published atomically
// (flushed under a temporary name, then hard-linked), so a reader sees a whole
// key or none and two creators agree on one value. A file that exists but does
// not hold a key is an error: minting another would orphan whatever was keyed
// with the first. A key left readable by group or others is still the same
// secret, so it is tightened to 0600 with a warning rather than refused, which
// would stop everything keyed with it; a mode that cannot be tightened is an
// error.
func (w *Workspace) SecretKey(name string) ([]byte, error) {
	if name == "" || name != filepath.Base(name) {
		return nil, errors.New("workspace: secret key name must be a plain file name")
	}
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(w.StateDir(), name)
	if key, err := readSecretKey(path); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = requireOwnerOnly(path)
		}
		if err != nil {
			return nil, err
		}
		return key, nil
	}
	raw := make([]byte, SecretKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := publishFile(path, []byte(hex.EncodeToString(raw)+"\n")); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	// Another creator may have won the race; its value is the key.
	return readSecretKey(path)
}

// requireOwnerOnly makes the key file readable by its owner alone and confirms
// the mode took.
func requireOwnerOnly(path string) error {
	if runtime.GOOS == "windows" {
		return nil // no POSIX mode bits to read back
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	slog.Warn("workspace: secret key file was readable by others; restricting it to its owner", "file", filepath.Base(path), "mode", info.Mode().Perm().String())
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if info, err = os.Stat(path); err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("workspace: " + filepath.Base(path) + " is readable by others and its mode cannot be restricted")
	}
	return nil
}

func readSecretKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != SecretKeyBytes {
		return nil, errors.New("workspace: " + filepath.Base(path) + " exists but does not hold a secret key")
	}
	return key, nil
}

// publishFile creates path with mode 0600 and the given content, or fails with
// fs.ErrExist when it is already there.
func publishFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Link(tmp.Name(), path)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

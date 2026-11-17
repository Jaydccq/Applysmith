package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// VerifyBackup checks a backup directory end to end: manifest shape, safe
// paths, no symlinks or unlisted files, every length and SHA-256, database
// integrity and foreign keys, and a supported schema. It reads only. A
// relative dir is taken from the working directory.
func VerifyBackup(ctx context.Context, dir string) (*Manifest, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	body, err := readRegular(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBackup, err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrInvalidBackup, err)
	}
	if m.Format != manifestFormat {
		return nil, fmt.Errorf("%w: unsupported manifest format %d", ErrInvalidBackup, m.Format)
	}
	if m.Database.Path != backupDBName {
		return nil, fmt.Errorf("%w: database entry must be %s", ErrInvalidBackup, backupDBName)
	}

	listed := map[string]bool{manifestName: true, backupDBName: true}
	checks := []struct {
		e    FileEntry
		path string
	}{{m.Database, entryPath(dir, m.Database, true)}}
	for _, e := range m.Files {
		if err := checkRelPath(e.Path); err != nil {
			return nil, err
		}
		if !included(e.Path) {
			return nil, fmt.Errorf("%w: %q is not a backed-up workspace location", ErrUnsafePath, e.Path)
		}
		key := backupFilesDir + "/" + e.Path
		if listed[key] {
			return nil, fmt.Errorf("%w: duplicate entry %s", ErrInvalidBackup, e.Path)
		}
		listed[key] = true
		checks = append(checks, struct {
			e    FileEntry
			path string
		}{e, entryPath(dir, e, false)})
	}
	if err := rejectUnlisted(dir, listed); err != nil {
		return nil, err
	}
	for _, c := range checks {
		if _, err := readRegularInfo(c.path); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidBackup, err)
		}
		sum, n, err := hashPath(ctx, c.path)
		if err != nil {
			return nil, err
		}
		if sum != c.e.SHA256 || n != c.e.Size {
			return nil, fmt.Errorf("%w: %s does not match its manifest hash", ErrInvalidBackup, c.e.Path)
		}
	}
	migs, err := loadMigrations(nil)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, backupDBName)
	if err := verifyDatabaseFile(ctx, dbPath, migs); err != nil {
		return nil, err
	}
	version, wsID, err := snapshotIdentity(ctx, dbPath, migs)
	if err != nil {
		return nil, err
	}
	if version != m.SchemaVersion || wsID != m.WorkspaceID {
		return nil, fmt.Errorf("%w: manifest schema/workspace does not match the database", ErrInvalidBackup)
	}
	return &m, nil
}

func readRegularInfo(p string) (fs.FileInfo, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	return info, nil
}

func readRegular(p string) ([]byte, error) {
	if _, err := readRegularInfo(p); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// rejectUnlisted fails on symlinks and on files the manifest does not name.
func rejectUnlisted(dir string, listed map[string]bool) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%w: symlink %s", ErrUnsafePath, rel)
		case d.IsDir():
			return nil
		case !listed[rel]:
			return fmt.Errorf("%w: %s is not in the manifest", ErrInvalidBackup, rel)
		}
		return nil
	})
}

// included reports whether a relative path lies in the WorkspaceIncludes
// allowlist, so a crafted manifest cannot place arbitrary files in a workspace.
func included(rel string) bool {
	for _, inc := range WorkspaceIncludes {
		if rel == inc || strings.HasPrefix(rel, inc+"/") {
			return true
		}
	}
	return false
}

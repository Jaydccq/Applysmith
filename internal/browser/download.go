package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDownloadName = 120
	maxDownloadExt  = 12
	maxCollisions   = 1000
)

// SafeDownloadName reduces a suggested file name to a single safe path
// element: no directory part, no control characters or any of < > : " | ? *,
// no leading dots, at most 120 characters while keeping an extension of at
// most 12 characters, and "download" when nothing is left.
func SafeDownloadName(suggested string) string {
	name := strings.ReplaceAll(suggested, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f, strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.TrimLeft(b.String(), ".")
	if name == "" {
		return "download"
	}
	if utf8.RuneCountInString(name) <= maxDownloadName {
		return name
	}
	ext := filepath.Ext(name)
	if utf8.RuneCountInString(ext) > maxDownloadExt+1 {
		ext = ""
	}
	stem := []rune(strings.TrimSuffix(name, ext))
	keep := maxDownloadName - utf8.RuneCountInString(ext)
	if keep < 1 {
		keep = 1
	}
	if len(stem) > keep {
		stem = stem[:keep]
	}
	return string(stem) + ext
}

// uniquePath returns a path in dir for name that does not exist: name,
// "name (1).ext", "name (2).ext", then after 1000 attempts a timestamp suffix.
func uniquePath(dir, name string, now time.Time) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	for i := 1; i <= maxCollisions; i++ {
		p = filepath.Join(dir, stem+" ("+strconv.Itoa(i)+")"+ext)
		if _, err := os.Lstat(p); err != nil {
			return p
		}
	}
	return filepath.Join(dir, stem+" ("+now.UTC().Format("20060102T150405.000000000")+")"+ext)
}

// moveStaged moves a completed staged download into dir under a safe, unused
// name and returns the final path. The final name is claimed with an
// exclusive create so two downloads cannot pick the same one.
func moveStaged(staged, dir, suggested string, now time.Time) (string, error) {
	name := SafeDownloadName(suggested)
	for range maxCollisions + 2 {
		dst := uniquePath(dir, name, now)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		_ = f.Close()
		if err := os.Rename(staged, dst); err != nil {
			_ = os.Remove(dst)
			return "", err
		}
		return dst, nil
	}
	return "", os.ErrExist
}

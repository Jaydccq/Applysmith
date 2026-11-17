//go:build darwin

package main

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Sizes are artifact sizes in bytes.
type Sizes struct {
	AppBundle      int64 `json:"appBundle"`
	AppBinary      int64 `json:"appBinary"`
	DiskImage      int64 `json:"diskImage"`
	CLIBinary      int64 `json:"cliBinary"`
	EngineBundle   int64 `json:"engineBundle"`
	ExtensionBuilt int64 `json:"extensionBundle"`
	DashboardAsset int64 `json:"dashboardAssets"`
}

func dirBytes(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func fileBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func measureSizes(repo, bundle, dmg, cli string) Sizes {
	gen := filepath.Join(repo, "internal", "engine", "generated")
	return Sizes{
		AppBundle:      dirBytes(bundle),
		AppBinary:      fileBytes(filepath.Join(bundle, "Contents", "MacOS", "Applysmith")),
		DiskImage:      fileBytes(dmg),
		CLIBinary:      fileBytes(cli),
		EngineBundle:   dirBytes(gen),
		ExtensionBuilt: dirBytes(filepath.Join(repo, "internal", "extension", "generated")),
		DashboardAsset: dirBytes(filepath.Join(repo, "web", "dist")),
	}
}

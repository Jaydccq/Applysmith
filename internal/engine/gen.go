//go:build ignore

// Generator for the embedded engine bundle: go generate ./internal/engine.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/engine/bundler"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "engine generate:", err)
		os.Exit(1)
	}
}

func run() error {
	out, err := bundler.Build(filepath.Join("..", "..", "engine"))
	if err != nil {
		return err
	}
	helper, err := bundler.BuildMainWorld(filepath.Join("..", "..", "engine"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll("generated", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join("generated", "mainworld.js"), helper, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("generated", "engine.js"), out, 0o644)
}

//go:build ignore

// Generator for the embedded extension: go generate ./internal/extension.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/extension/builder"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "extension generate:", err)
		os.Exit(1)
	}
}

func run() error {
	out, err := builder.Build(filepath.Join("..", "..", "extension"), filepath.Join("..", "..", "engine"))
	if err != nil {
		return err
	}
	if err := os.RemoveAll("generated"); err != nil {
		return err
	}
	if err := os.MkdirAll("generated", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join("generated", ".gitkeep"), nil, 0o644); err != nil {
		return err
	}
	for name, body := range out.Files {
		dst := filepath.Join("generated", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

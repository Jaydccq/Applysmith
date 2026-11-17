//go:build !smoke

package main

import (
	"bytes"
	"os"
	"testing"
)

// The page script of the smoke test is for development builds only.
func TestReleaseBuildsDoNotCarryTheSmokeScript(t *testing.T) {
	if smokeBuilt || smokeScript != "" {
		t.Fatal("a build without the smoke tag has the smoke script")
	}
	src, err := os.ReadFile("smoke_on.go")
	if err != nil || !bytes.HasPrefix(src, []byte("//go:build smoke")) {
		t.Fatalf("the embed must sit behind the smoke tag: %v", err)
	}
}

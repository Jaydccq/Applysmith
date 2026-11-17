package toolchain

import (
	"testing"

	"applysmith/internal/documents/assets"
)

func mustAsset(t *testing.T, name string) []byte {
	t.Helper()
	b, err := assets.Read(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

package agent

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed pi_extension.mjs
var extensionSource []byte

// writeExtension puts the embedded extension in the agent directory and returns
// its path. It is replaced whole at every start so it matches this binary.
func writeExtension(agentDir string) (string, error) {
	dir := filepath.Join(agentDir, "extension")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "applysmith.mjs")
	return path, replaceFile(path, extensionSource)
}

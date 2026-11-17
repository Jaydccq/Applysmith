package remote

import (
	"os"
	"path/filepath"
)

func writePolicy(root, body string) error {
	return os.WriteFile(filepath.Join(root, "policy.yml"), []byte(body), 0o600)
}

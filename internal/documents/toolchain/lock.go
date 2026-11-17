// Package toolchain detects, sets up and proves the external tools that build
// documents: LuaLaTeX and XeLaTeX with the reviewed packages, a private Python
// venv with hash-locked pypdf, and Poppler. Detection only reads; the explicit
// setup and smoke actions are the only code that writes under the toolchain
// root. Generation never installs anything.
package toolchain

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed lock.json
var lockJSON []byte

// Lock is the reviewed pin set: what was installed and proven by the smoke
// that accepted these versions. It holds no hash of the embedded assets; those
// are measured from the embedded files themselves.
type Lock struct {
	SchemaVersion int `json:"schema_version"`
	Python        struct {
		Min string `json:"min"`
		// Verified lists the interpreters the verifier scripts were run on.
		Verified []string `json:"verified"`
	} `json:"python"`
	Pypdf struct {
		Version     string `json:"version"`
		WheelSHA256 string `json:"wheel_sha256"`
	} `json:"pypdf"`
	TeX struct {
		Distribution string `json:"distribution"`
		Moderncv     string `json:"moderncv"`
		// Packages maps a TeX package to one file that proves it is installed.
		Packages []TeXPackage `json:"packages"`
	} `json:"tex"`
	Poppler struct {
		Min    string `json:"min"`
		Tested string `json:"tested"`
	} `json:"poppler"`
}

// TeXPackage is one package of the dependency closure.
type TeXPackage struct {
	Name    string `json:"name"`
	Witness string `json:"witness"`
	// Revision is the TeX Live database revision the smoke was proven with.
	Revision string `json:"revision"`
}

var lock = mustLock()

func mustLock() Lock {
	var l Lock
	if err := json.Unmarshal(lockJSON, &l); err != nil {
		panic(fmt.Sprintf("toolchain: lock.json: %v", err))
	}
	return l
}

// versionLess compares dotted numeric versions; a missing part is zero.
func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func versionParts(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

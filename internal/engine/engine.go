// Package engine holds the in-page autofill engine as an embedded script.
// The script is built by `go generate ./internal/engine` (make generate) and
// is not committed; on a fresh checkout Bundle is empty and Stamp is "".
package engine

//go:generate go run gen.go

import (
	"embed"
	"sync"

	"applysmith/internal/engine/stamp"
)

//go:embed all:generated
var generated embed.FS

var bundle = sync.OnceValue(func() []byte {
	b, err := generated.ReadFile("generated/engine.js")
	if err != nil {
		return nil
	}
	return b
})

var helper = sync.OnceValue(func() []byte {
	b, err := generated.ReadFile("generated/mainworld.js")
	if err != nil {
		return nil
	}
	return b
})

// MainWorldBundle returns the main-world helper script with its two
// placeholders (the nonce and the stamp) still in it, or nil when the script
// has not been generated.
func MainWorldBundle() []byte { return helper() }

// MainWorldStamp is the content hash of the helper, which names the instance
// that is installed in a page.
func MainWorldStamp() string {
	b := helper()
	if len(b) == 0 {
		return ""
	}
	return stamp.HashOf(b)
}

// Bundle returns the self-executing engine script, ending with the call that
// sets its stamp, or nil when the script has not been generated.
func Bundle() []byte { return bundle() }

// Stamp returns the content hash of the embedded bundle, which the running
// engine reports in its status. It is "" when the bundle is missing or its
// trailer is damaged.
func Stamp() string { return stampOnce() }

var stampOnce = sync.OnceValue(func() string {
	s, err := stamp.Of(bundle())
	if err != nil {
		return ""
	}
	return s
})

// Package stamp defines the content stamp of the engine bundle: a short hash
// appended as a trailer that sets the running engine's stamp. It is separate
// from the bundler so that the host, which only reads stamps, does not link
// the bundler's build tooling.
package stamp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Len is the number of hexadecimal characters of the content hash kept as the
// stamp.
const Len = 16

const (
	trailerPrefix = "\n__applysmithEngine.setStamp(\""
	trailerSuffix = "\");\n"
)

// HashOf returns the stamp of a bundle body: the first Len lowercase
// hexadecimal characters of its SHA-256.
func HashOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:Len]
}

// Append returns body followed by the trailer that stamps it.
func Append(body []byte) []byte {
	return append(append(append(append([]byte{}, body...), trailerPrefix...), HashOf(body)...), trailerSuffix...)
}

// Of reads the stamp from a built bundle and checks that it matches the
// bundle bytes before the trailer.
func Of(bundle []byte) (string, error) {
	n := len(trailerPrefix) + Len + len(trailerSuffix)
	if len(bundle) < n {
		return "", errors.New("bundle is shorter than its stamp trailer")
	}
	body, trailer := bundle[:len(bundle)-n], string(bundle[len(bundle)-n:])
	stamp := trailer[len(trailerPrefix) : len(trailerPrefix)+Len]
	if trailer != trailerPrefix+stamp+trailerSuffix {
		return "", errors.New("bundle has no stamp trailer")
	}
	if want := HashOf(body); stamp != want {
		return "", fmt.Errorf("stamp %s does not match bundle hash %s", stamp, want)
	}
	return stamp, nil
}

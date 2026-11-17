//go:build !unix

package store

import "errors"

var errNoSpace = errors.New("no space left on device")

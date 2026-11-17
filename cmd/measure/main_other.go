//go:build !darwin

// Command measure records performance on macOS only: it launches the packaged
// macOS app and reads its WebView helpers.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "measure: supported on macOS only")
	os.Exit(2)
}

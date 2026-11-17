// Command releasecheck runs the publication pipeline of internal/release:
// `make release-check` runs it on the current tree and `make publish-tree`
// stops after the exported tree has passed the release-mode guard. Nothing is
// pushed or published.
package main

import (
	"flag"
	"fmt"
	"os"

	"applysmith/internal/release"
)

func main() {
	repo := flag.String("repo", ".", "the private repository to check")
	publishTree := flag.String("publish-tree", "", "export into this directory and run the release-mode guard on it, then stop")
	keep := flag.Bool("keep", false, "keep the temporary export after a passing run")
	flag.Parse()

	cfg := release.DefaultConfig(*repo)
	cfg.Log = os.Stderr
	cfg.Keep = *keep
	if *publishTree != "" {
		cfg.PublishTree = true
		cfg.RequireGitleaks = false
		cfg.Out = *publishTree
	}
	rep, err := release.Run(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasecheck:", err)
		os.Exit(2)
	}
	rep.Print(os.Stdout)
	if rep.Failed() {
		os.Exit(1)
	}
}

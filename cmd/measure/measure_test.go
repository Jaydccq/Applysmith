//go:build darwin

package main

import (
	"strings"
	"testing"
)

// The suite timings come from `go test -json`, a format this tool does not own.
func TestParseEvents_OneOutcomePerTestAndAnUnparsableLineIsSkipped(t *testing.T) {
	in := `{"Action":"run","Package":"p","Test":"TestA"}
not json
{"Action":"pass","Package":"p","Test":"TestA/sub","Elapsed":0.5}
{"Action":"pass","Package":"p","Test":"TestA","Elapsed":1.5}
{"Action":"output","Package":"p","Test":"TestB","Output":"    b_test.go:9: boom\n"}
{"Action":"fail","Package":"p","Test":"TestB","Elapsed":0.25}
{"Action":"skip","Package":"p","Test":"TestC"}
{"Action":"fail","Package":"p","Elapsed":3}
`
	outs, pkgS, fails := parseEvents(strings.NewReader(in))
	if len(outs) != 4 || pkgS != 3 || len(fails) != 2 {
		t.Fatalf("outs %d, package seconds %v, failures %+v", len(outs), pkgS, fails)
	}
	if outs[0].Test != "TestA/sub" || outs[0].ElapsedMS != 500 || outs[2].Result != "fail" {
		t.Fatalf("outcomes %+v", outs)
	}
	if fails[0].Test != "TestB" || len(fails[0].Tail) != 1 || !strings.Contains(fails[0].Tail[0], "boom") || fails[1].Test != "" {
		t.Fatalf("failures %+v", fails)
	}
}

func TestReplaceBlock_KeepsTheProseAroundTheMarkers(t *testing.T) {
	page := "intro\n" + beginMark + "\nold\n" + endMark + "\noutro\n"
	got, err := replaceBlock(page, "new")
	if err != nil || !strings.HasPrefix(got, "intro\n") || !strings.HasSuffix(got, endMark+"\noutro\n") || strings.Contains(got, "old") || !strings.Contains(got, "\nnew\n") {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := replaceBlock("no markers", "x"); err == nil {
		t.Fatal("a page without markers is refused")
	}
}

// A suite with no runs has no last run to report, so a run count below one is
// refused before anything is measured.
func TestMeasureAll_RefusesFewerThanOneSuiteRun(t *testing.T) {
	dir := t.TempDir()
	err := measureAll(dir, "", "", "", 1, 0, 0, Load{}, "results.json", "page.md", "none", "")
	if err == nil || !strings.Contains(err.Error(), "-runs") {
		t.Fatalf("-runs 0: %v", err)
	}
}

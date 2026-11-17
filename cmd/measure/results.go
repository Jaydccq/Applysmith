//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Params are the knobs of one measurement run.
type Params struct {
	StartSamples int     `json:"startupSamplesPerCase"`
	SuiteRuns    int     `json:"suiteRuns"`
	SettleS      float64 `json:"memorySettleSeconds"`
	MaxLoad      float64 `json:"maxLoad1"`
	LoadWaitS    float64 `json:"loadWaitSeconds"`
}

// SuiteResult is every run of one suite.
type SuiteResult struct {
	Name     string     `json:"name"`
	Command  string     `json:"command"`
	Runs     []SuiteRun `json:"runs"`
	WallS    Summary    `json:"wallSeconds"`
	PeakTree Summary    `json:"peakTreeMB"`
	Chrome   Summary    `json:"peakChromeMB"`
}

// Results is the machine-readable record, committed beside the page.
type Results struct {
	Schema    int           `json:"schema"`
	Generated string        `json:"generated"`
	Env       Environment   `json:"environment"`
	Params    Params        `json:"parameters"`
	LoadStart float64       `json:"load1AtStart"`
	LoadEnd   float64       `json:"load1AtEnd"`
	Sizes     Sizes         `json:"sizesBytes"`
	Startup   []StartResult `json:"startup"`
	Suites    []SuiteResult `json:"suites"`
	Workloads []Workload    `json:"workloads"`
	Orphans   []string      `json:"leftoverProcesses"`
}

func summarizeSuite(name, command string, runs []SuiteRun) SuiteResult {
	r := SuiteResult{Name: name, Command: command, Runs: runs}
	var wall, tree, chrome []float64
	for _, x := range runs {
		wall = append(wall, x.WallS)
		tree = append(tree, x.PeakMemory.TotalMB)
		chrome = append(chrome, x.PeakChrome)
	}
	r.WallS, r.PeakTree, r.Chrome = summarize(wall), summarize(tree), summarize(chrome)
	return r
}

func writeJSON(path string, r Results) error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

const (
	beginMark = "<!-- measure:begin -->"
	endMark   = "<!-- measure:end -->"
)

// replaceBlock puts block between the markers of the page, keeping the prose
// around them.
func replaceBlock(page, block string) (string, error) {
	i, j := strings.Index(page, beginMark), strings.Index(page, endMark)
	if i < 0 || j < i {
		return "", fmt.Errorf("the page has no %s ... %s block", beginMark, endMark)
	}
	return page[:i+len(beginMark)] + "\n\n" + block + "\n" + page[j:], nil
}

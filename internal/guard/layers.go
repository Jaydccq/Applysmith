package guard

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	ruleLayers = "package-layers"
	rulePure   = "pure-packages"

	layersFile   = "internal/guard/layers.txt"
	frameworkPkg = "github.com/egoist/mygo"
)

// layer is the entry of one package in layers.txt.
type layer struct {
	n    int
	pure bool
}

// parseLayers reads lines of "<package path> <layer number> [pure]".
func parseLayers(content string) (map[string]layer, []Finding) {
	layers := map[string]layer{}
	var problems []Finding
	for i, raw := range strings.Split(content, "\n") {
		line, _, _ := strings.Cut(raw, "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		bad := func(detail string) {
			problems = append(problems, Finding{Rule: ruleLayers, Path: layersFile, Line: i + 1, Detail: detail})
		}
		if len(f) < 2 || len(f) > 3 {
			bad("expected: <package path> <layer number> [pure]")
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 0 {
			bad("layer must be a non-negative integer")
			continue
		}
		if len(f) == 3 && f[2] != "pure" {
			bad("third field, when present, must be \"pure\"")
			continue
		}
		if _, dup := layers[f[0]]; dup {
			bad("package listed twice")
			continue
		}
		layers[f[0]] = layer{n: n, pure: len(f) == 3}
	}
	return layers, problems
}

// pkgInfo is the import data `go list` reports for one package.
type pkgInfo struct {
	Dir         string // relative to the module root, "." for the root package
	Imports     []string
	TestImports []string
}

type packageList struct {
	list []pkgInfo
	err  error
}

// listPackages runs go list over the whole module, tolerating packages whose
// embedded assets have not been generated.
func listPackages(root, module string) ([]pkgInfo, error) {
	cmd := exec.Command("go", "list", "-e", "-f",
		`{{.ImportPath}}{{"\t"}}{{join .Imports " "}}{{"\t"}}{{join .TestImports " "}}{{"\t"}}{{join .XTestImports " "}}`, "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var pkgs []pkgInfo
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) != 4 || cols[0] == "" {
			continue
		}
		dir := strings.TrimPrefix(strings.TrimPrefix(cols[0], module), "/")
		if dir == "" {
			dir = "."
		}
		pkgs = append(pkgs, pkgInfo{Dir: dir, Imports: strings.Fields(cols[1]), TestImports: append(strings.Fields(cols[2]), strings.Fields(cols[3])...)})
	}
	return pkgs, nil
}

func underDir(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// checkLayerGraph applies the layer, framework-confinement and listing rules.
func checkLayerGraph(module string, layers map[string]layer, pkgs []pkgInfo) []Finding {
	var out []Finding
	add := func(dir, detail string) { out = append(out, Finding{Rule: ruleLayers, Path: dir, Detail: detail}) }
	for _, p := range pkgs {
		for _, imp := range append(append([]string{}, p.Imports...), p.TestImports...) {
			if underDir(imp, frameworkPkg) && !underDir(p.Dir, "app") {
				add(p.Dir, "imports the desktop framework; only packages under app/ may")
			}
		}
		if !underDir(p.Dir, "internal") {
			continue
		}
		own, listed := layers[p.Dir]
		if !listed {
			add(p.Dir, "package is not listed in "+layersFile)
			continue
		}
		for _, imp := range p.Imports {
			rel, ok := strings.CutPrefix(imp, module+"/")
			if !ok {
				continue
			}
			switch {
			case underDir(rel, "cmd") || underDir(rel, "app"):
				add(p.Dir, fmt.Sprintf("imports %s; internal packages must not import cmd/ or app/", rel))
			case underDir(rel, "internal"):
				dep, depListed := layers[rel]
				if !depListed {
					add(p.Dir, fmt.Sprintf("imports %s, which is not listed in %s", rel, layersFile))
				} else if dep.n >= own.n {
					add(p.Dir, fmt.Sprintf("layer %d imports %s at layer %d; imports must go to a strictly lower layer", own.n, rel, dep.n))
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

var impureImports = map[string]bool{
	"os": true, "io": true, "io/fs": true, "net": true, "net/http": true, "os/exec": true, "syscall": true,
	"database/sql": true, "time": true, "math/rand": true, "math/rand/v2": true, "crypto/rand": true,
}

// checkPureGraph keeps packages flagged "pure" free of I/O, clock and
// randomness. Test files are not part of Imports and so are exempt.
func checkPureGraph(module string, layers map[string]layer, pkgs []pkgInfo) []Finding {
	var out []Finding
	for _, p := range pkgs {
		if !layers[p.Dir].pure {
			continue
		}
		for _, imp := range p.Imports {
			rel, internal := strings.CutPrefix(imp, module+"/")
			switch {
			case impureImports[imp]:
				out = append(out, Finding{Rule: rulePure, Path: p.Dir, Detail: "imports " + imp})
			case internal && !layers[rel].pure:
				out = append(out, Finding{Rule: rulePure, Path: p.Dir, Detail: "imports " + rel + ", which is not on the pure list"})
			}
		}
	}
	return out
}

// graphInputs loads layers.txt, the module path and the package graph.
func graphInputs(t *Tree) (module string, layers map[string]layer, pkgs []pkgInfo, problems []Finding, err error) {
	mod, err := t.Read("go.mod")
	if err != nil {
		return "", nil, nil, nil, err
	}
	for _, l := range strings.Split(string(mod), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
			module = strings.TrimSpace(m)
		}
	}
	data, err := t.Read(layersFile)
	if err != nil {
		return "", nil, nil, nil, err
	}
	layers, problems = parseLayers(string(data))
	if t.packages == nil {
		list, err := listPackages(t.Root, module)
		t.packages = &packageList{list, err}
	}
	return module, layers, t.packages.list, problems, t.packages.err
}

func checkLayers(t *Tree) ([]Finding, error) {
	module, layers, pkgs, problems, err := graphInputs(t)
	if err != nil {
		return nil, err
	}
	return append(problems, checkLayerGraph(module, layers, pkgs)...), nil
}

func checkPurity(t *Tree) ([]Finding, error) {
	module, layers, pkgs, problems, err := graphInputs(t)
	if err != nil {
		return nil, err
	}
	_ = problems // reported by the layers rule
	return checkPureGraph(module, layers, pkgs), nil
}

package match

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// Route is one row of the pattern route catalog. Lower Priority runs first and
// no two routes share one. A route names its target entry id; it never means
// "the first entry of some kind".
//
// Pattern, Exclude and AllOf are RE2 sources compiled case-insensitively. AllOf
// replaces a lookahead conjunction: every listed pattern must also match.
// Veto and Require hold the constructs RE2 cannot express (a lookbehind, a
// length cap, "no link word after the last count phrase") as plain code.
type Route struct {
	Priority        int
	Target          string
	Pattern         string
	Exclude         string
	AllOf           []string
	RequiresOptions bool
	// Tag names a variant the resolvers must know about, for example
	// "negated-sponsorship", whose target is the positive sponsorship entry.
	Tag     string
	Veto    func(text string) bool
	Require func(text string) bool
}

type compiledRoute struct {
	Route
	re  *regexp.Regexp
	ex  *regexp.Regexp
	all []*regexp.Regexp
}

func compileRoute(r Route) (compiledRoute, error) {
	var c compiledRoute
	c.Route = r
	if r.Target == "" {
		return c, fmt.Errorf("route %d has no target entry id", r.Priority)
	}
	var err error
	if c.re, err = regexp.Compile("(?i)" + r.Pattern); err != nil {
		return c, fmt.Errorf("route %d (%s) pattern: %w", r.Priority, r.Target, err)
	}
	if r.Exclude != "" {
		if c.ex, err = regexp.Compile("(?i)" + r.Exclude); err != nil {
			return c, fmt.Errorf("route %d (%s) exclusion: %w", r.Priority, r.Target, err)
		}
	}
	for _, p := range r.AllOf {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return c, fmt.Errorf("route %d (%s) all-of: %w", r.Priority, r.Target, err)
		}
		c.all = append(c.all, re)
	}
	return c, nil
}

// compileRoutes validates and compiles a route list: every rule compiles,
// priorities are unique, and the result is sorted by priority.
func compileRoutes(rs []Route) ([]compiledRoute, error) {
	sorted := slices.Clone(rs)
	slices.SortStableFunc(sorted, func(a, b Route) int { return a.Priority - b.Priority })
	out := make([]compiledRoute, 0, len(sorted))
	for i, r := range sorted {
		if i > 0 && sorted[i-1].Priority == r.Priority {
			return nil, fmt.Errorf("routes %q and %q share priority %d", sorted[i-1].Target, r.Target, r.Priority)
		}
		c, err := compileRoute(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// patternHit reports whether the route's pattern (and every all-of pattern)
// matches the text, ignoring exclusions.
func (c *compiledRoute) patternHit(text string) bool {
	if !c.re.MatchString(text) {
		return false
	}
	for _, re := range c.all {
		if !re.MatchString(text) {
			return false
		}
	}
	return c.Require == nil || c.Require(text)
}

// vetoes reports whether the exclusion pattern or the veto predicate fires.
func (c *compiledRoute) vetoes(text string) bool {
	if c.ex != nil && c.ex.MatchString(text) {
		return true
	}
	return c.Veto != nil && c.Veto(text)
}

// Matches reports whether the route claims the text on its own: the pattern
// hits and nothing vetoes. The text is folded the way the walk folds it.
func (c *compiledRoute) Matches(text string) bool {
	t := routeText(text)
	return c.patternHit(t) && !c.vetoes(t)
}

// span returns the part of the text the main pattern matched.
func (c *compiledRoute) span(text string) string { return c.re.FindString(text) }

// routeCatalogRaw is the full catalog before compilation. Each family lives in
// its own file; this list is the only place that names them.
func routeCatalogRaw() []Route {
	var all []Route
	for _, part := range [][]Route{
		fixedRoutesA(),
		fixedRoutesB(),
		pickRoutes(),
		otherKindRoutes(),
		phrasingRoutes(),
	} {
		all = append(all, part...)
	}
	return all
}

var catalog = sync.OnceValues(func() ([]compiledRoute, error) {
	return compileRoutes(routeCatalogRaw())
})

// routeCatalog returns the compiled catalog. A catalog that does not compile is
// a build error: the catalog test fails before any release.
func routeCatalog() []compiledRoute {
	c, err := catalog()
	if err != nil {
		panic("match: route catalog: " + err.Error())
	}
	return c
}

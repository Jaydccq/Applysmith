package match

// routeEnv is what the walk needs from the request: whether the control renders
// a real option, how to find an entry, and whether the control accepts it.
type routeEnv struct {
	hasOptions bool
	lookup     func(id string) *Entry
	accepts    func(e *Entry) (ok bool, why string)
}

type routeSkip struct {
	Priority int
	ID       string
	Why      string
}

type routeWalk struct {
	Hit *compiledRoute
	// Blocked holds the entries a route would have claimed but an exclusion
	// vetoed; the fuzzy stage and the topic lane must not offer them for this
	// question.
	Blocked map[string]bool
	Skipped []routeSkip
	// Released are the stemmed tokens of the spans claimed for entries the
	// control cannot accept; the fuzzy stage counts only seeds holding them all.
	Released []string
}

// walkRoutes runs the route table: the page's own wording first, then, for the
// rules that did not match it, the reworded form. The first rule that matches,
// is not vetoed, whose entry exists and is accepted by the control, wins.
func walkRoutes(rs []compiledRoute, page, reworded string, env routeEnv) routeWalk {
	w := routeWalk{Blocked: map[string]bool{}}
	page = routeText(page)
	pageHit := make([]bool, len(rs))
	for pass, text := range []string{page, routeText(reworded)} {
		if pass == 1 && (reworded == "" || routeText(reworded) == page) {
			break
		}
		for i := range rs {
			r := &rs[i]
			if pass == 1 && pageHit[i] {
				continue
			}
			if r.RequiresOptions && !env.hasOptions {
				continue
			}
			if !r.patternHit(text) {
				continue
			}
			if pass == 0 {
				pageHit[i] = true
			}
			if r.vetoes(text) || r.vetoes(page) {
				w.Blocked[r.Target] = true
				continue
			}
			e := env.lookup(r.Target)
			if e == nil {
				continue
			}
			if ok, why := env.accepts(e); !ok {
				w.Skipped = append(w.Skipped, routeSkip{r.Priority, r.Target, why})
				w.Released = append(w.Released, stemmedTokens(r.span(text))...)
				continue
			}
			w.Hit = r
			return w
		}
	}
	return w
}

package tracker

import (
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/identity"
)

// cluster is a dedup group in the making: units of rows that share an identity
// key or alias, all of which match every other unit of the cluster.
type cluster struct {
	units     [][]int64
	ambiguous []string
}

func (c *cluster) ids() []int64 {
	var out []int64
	for _, u := range c.units {
		out = append(out, u...)
	}
	slices.Sort(out)
	return out
}

// clusterRows partitions rows into clusters whose members are pairwise
// compatible, so a row that matches two requisitions can never chain them
// together. Rows sharing an identity key or alias form one unit. Units are
// taken in order of their lowest row id; each joins the first cluster it
// matches in full, else starts a new one. A unit that fits a second cluster
// as well is a bridge: the cluster holding it is marked for the user and is
// never merged automatically, because it is unknown which requisition the
// bridge belongs to.
func clusterRows(cands []candidate, same func(a, b candidate) bool) []*cluster {
	byID := make(map[int64]candidate, len(cands))
	for _, c := range cands {
		byID[c.id] = c
	}
	match := map[[2]int64]bool{}
	// near lists the rows each row matches.
	near := map[int64][]int64{}
	byCompany := map[string][]int64{}
	for _, c := range cands {
		for _, name := range identity.CompanyKeys(c.company) {
			byCompany[name] = append(byCompany[name], c.id)
		}
	}
	for _, ids := range byCompany {
		for i, a := range ids {
			for _, b := range ids[i+1:] {
				if same(byID[a], byID[b]) {
					match[[2]int64{a, b}] = true
					near[a], near[b] = append(near[a], b), append(near[b], a)
				}
			}
		}
	}
	matches := func(a, b int64) bool {
		if a > b {
			a, b = b, a
		}
		return match[[2]int64{a, b}]
	}
	fits := func(unit []int64, c *cluster) bool {
		for _, a := range unit {
			for _, u := range c.units {
				for _, b := range u {
					if !matches(a, b) {
						return false
					}
				}
			}
		}
		return true
	}

	var clusters []*cluster
	// at is the cluster each placed row is in. A unit fits a cluster in full
	// only if the cluster holds a row its first row matches, so those clusters
	// are the only ones tried, in the order they were made.
	at := map[int64]int{}
	tried := func(unit []int64) []int {
		var out []int
		for _, b := range near[unit[0]] {
			if i, ok := at[b]; ok {
				out = append(out, i)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	for _, unit := range sharedKeyUnits(cands) {
		home := -1
		for _, i := range tried(unit) {
			if fits(unit, clusters[i]) {
				home = i
				break
			}
		}
		if home < 0 {
			clusters = append(clusters, &cluster{})
			home = len(clusters) - 1
		}
		clusters[home].units = append(clusters[home].units, unit)
		for _, id := range unit {
			at[id] = home
		}
	}
	for hi, home := range clusters {
		if len(home.units) < 2 {
			continue
		}
		for _, unit := range home.units {
			for _, oi := range tried(unit) {
				other := clusters[oi]
				if oi == hi || !fits(unit, other) {
					continue
				}
				note := fmt.Sprintf("%s also matches %s, which this group does not hold", rowList(unit), rowList(other.ids()))
				home.ambiguous = append(home.ambiguous, note)
			}
		}
	}
	return clusters
}

// sharedKeyUnits groups rows that share an identity key or alias, each unit
// sorted by id, the units ordered by their lowest id.
func sharedKeyUnits(cands []candidate) [][]int64 {
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	byKey := map[string]int64{}
	for _, c := range cands {
		parent[c.id] = c.id
	}
	for _, c := range cands {
		for _, k := range append([]string{c.key}, c.aliases...) {
			first, seen := byKey[k]
			if !seen {
				byKey[k] = c.id
				continue
			}
			ra, rb := find(first), find(c.id)
			if ra != rb {
				if rb < ra {
					ra, rb = rb, ra
				}
				parent[rb] = ra
			}
		}
	}
	units := map[int64][]int64{}
	var roots []int64
	for _, c := range cands { // ascending id
		r := find(c.id)
		if _, ok := units[r]; !ok {
			roots = append(roots, r)
		}
		units[r] = append(units[r], c.id)
	}
	out := make([][]int64, 0, len(roots))
	for _, r := range roots {
		out = append(out, units[r])
	}
	return out
}

func rowList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

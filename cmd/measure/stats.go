//go:build darwin

package main

import (
	"math"
	"sort"
)

// Summary is the median and 90th percentile of a sample set. The median of an
// even count is the mean of the two middle values; the percentile is the
// nearest rank, so neither invents a value no sample had.
type Summary struct {
	N      int     `json:"n"`
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	Max    float64 `json:"max"`
}

func summarize(xs []float64) Summary {
	if len(xs) == 0 {
		return Summary{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	med := s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	rank := int(math.Ceil(0.9*float64(n))) - 1
	return Summary{N: n, Min: s[0], Median: med, P90: s[rank], Max: s[n-1]}
}

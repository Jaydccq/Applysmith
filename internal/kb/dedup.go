package kb

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// numberWords maps the spelled numbers one to twenty to their digits.
var numberWords = func() map[string]string {
	digits := map[string]string{}
	for n, w := range strings.Fields("one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty") {
		digits[w] = strconv.Itoa(n + 1)
	}
	return digits
}()

var (
	wordRE   = lazyregexp.New(`[A-Za-z]+`)
	metricRE = lazyregexp.New(`(\d+(?:[.,]\d+)*)\s*(%|[A-Za-z][A-Za-z-]*)`)
)

// fingerprint identifies the event a set of facts describes.
type fingerprint struct {
	metrics map[string]bool
	stems   map[string]bool
}

// digitize rewrites the number words one to twenty as digits.
func digitize(s string) string {
	return wordRE.ReplaceAllStringFunc(s, func(w string) string {
		if d, ok := numberWords[strings.ToLower(w)]; ok {
			return d
		}
		return w
	})
}

func fingerprintOf(facts []string) fingerprint {
	fp := fingerprint{metrics: map[string]bool{}, stems: map[string]bool{}}
	text := digitize(strings.Join(facts, "\n"))
	for _, m := range metricRE.FindAllStringSubmatch(text, -1) {
		num := strings.ReplaceAll(m[1], ",", "")
		noun := strings.ToLower(m[2])
		if noun == "percent" {
			noun = "%"
		}
		if !distinctiveNumber(num, noun) {
			continue
		}
		if noun != "%" {
			if stopWords[noun] {
				continue
			}
			noun = fold(noun)
		}
		fp.metrics[num+" "+noun] = true
	}
	for _, t := range Tokens(text) {
		fp.stems[t] = true
	}
	return fp
}

// distinctiveNumber leaves out the numbers that name nothing in particular: 0,
// 1 and calendar years.
func distinctiveNumber(num, noun string) bool {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v <= 1 {
		return false
	}
	if noun != "%" && v >= 1900 && v <= 2100 && !strings.ContainsAny(num, ".") {
		return false
	}
	return true
}

const (
	sharedMetricsForDuplicate = 3
	jaccardForDuplicate       = 0.4
)

// sameEvent reports whether two fingerprints describe one event: at least
// three shared metric tokens, or a Jaccard overlap of the fact stems of at
// least 0.4. A side with no facts is never compared.
func sameEvent(a, b fingerprint) bool {
	if len(a.stems) == 0 || len(b.stems) == 0 {
		return false
	}
	shared := 0
	for m := range a.metrics {
		if b.metrics[m] {
			shared++
		}
	}
	if shared >= sharedMetricsForDuplicate {
		return true
	}
	inter := 0
	for t := range a.stems {
		if b.stems[t] {
			inter++
		}
	}
	union := len(a.stems) + len(b.stems) - inter
	return union > 0 && float64(inter)/float64(union) >= jaccardForDuplicate
}

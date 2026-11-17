// Package lazyregexp compiles a regular expression declared at package level
// the first time it is used, instead of when the program starts.
//
// Applysmith declares about 2,400 expressions at package level. Compiled at start
// they cost every process (the server, the app, each run of the command line)
// about 55 MB of allocation and 19 MB of live heap before it does any work, and
// most processes use few of them (docs/architecture/performance.md, "Process
// start").
//
// Inside a test binary every expression compiles when it is declared, as
// regexp.MustCompile does, so a malformed pattern still fails every test that
// links the package declaring it.
package lazyregexp

import (
	"regexp"
	"sync"
	"testing"
)

// Regexp is a regular expression compiled on its first use. Its methods are
// those of regexp.Regexp that Applysmith calls.
type Regexp struct {
	str  string
	once sync.Once
	rx   *regexp.Regexp
}

// New returns the expression str, compiled when it is first used. A malformed
// str panics then, as regexp.MustCompile would at once.
func New(str string) *Regexp {
	r := &Regexp{str: str}
	if testing.Testing() {
		r.re()
	}
	return r
}

func (r *Regexp) re() *regexp.Regexp {
	r.once.Do(r.build)
	return r.rx
}

func (r *Regexp) build() { r.rx = regexp.MustCompile(r.str) }

// String is the source text, without compiling it.
func (r *Regexp) String() string { return r.str }

func (r *Regexp) Match(b []byte) bool                  { return r.re().Match(b) }
func (r *Regexp) MatchString(s string) bool            { return r.re().MatchString(s) }
func (r *Regexp) FindString(s string) string           { return r.re().FindString(s) }
func (r *Regexp) FindStringIndex(s string) []int       { return r.re().FindStringIndex(s) }
func (r *Regexp) FindSubmatch(b []byte) [][]byte       { return r.re().FindSubmatch(b) }
func (r *Regexp) FindStringSubmatch(s string) []string { return r.re().FindStringSubmatch(s) }
func (r *Regexp) FindStringSubmatchIndex(s string) []int {
	return r.re().FindStringSubmatchIndex(s)
}
func (r *Regexp) FindAll(b []byte, n int) [][]byte { return r.re().FindAll(b, n) }
func (r *Regexp) FindAllString(s string, n int) []string {
	return r.re().FindAllString(s, n)
}
func (r *Regexp) FindAllStringIndex(s string, n int) [][]int {
	return r.re().FindAllStringIndex(s, n)
}
func (r *Regexp) FindAllSubmatch(b []byte, n int) [][][]byte {
	return r.re().FindAllSubmatch(b, n)
}
func (r *Regexp) FindAllStringSubmatch(s string, n int) [][]string {
	return r.re().FindAllStringSubmatch(s, n)
}
func (r *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	return r.re().FindAllStringSubmatchIndex(s, n)
}
func (r *Regexp) ReplaceAll(src, repl []byte) []byte { return r.re().ReplaceAll(src, repl) }
func (r *Regexp) ReplaceAllString(src, repl string) string {
	return r.re().ReplaceAllString(src, repl)
}
func (r *Regexp) ReplaceAllLiteralString(src, repl string) string {
	return r.re().ReplaceAllLiteralString(src, repl)
}
func (r *Regexp) ReplaceAllStringFunc(src string, repl func(string) string) string {
	return r.re().ReplaceAllStringFunc(src, repl)
}
func (r *Regexp) Split(s string, n int) []string { return r.re().Split(s, n) }

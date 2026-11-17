package lazyregexp

import (
	"testing"
)

// A test binary compiles every expression when it is declared, so a malformed
// pattern fails the tests of its package, as regexp.MustCompile would.
func TestNewCompilesAtOnceInATestBinary(t *testing.T) {
	if r := New(`a+`); r.rx == nil {
		t.Fatal("the expression waited for its first use in a test binary")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a malformed pattern was accepted")
		}
	}()
	New(`(`)
}

// Outside a test binary the expression compiles on its first use, once.
func TestAnExpressionCompilesOnItsFirstUse(t *testing.T) {
	r := &Regexp{str: `b+`}
	if r.String() != `b+` || r.rx != nil {
		t.Fatal("String compiled the expression")
	}
	if !r.MatchString("abbc") || r.rx == nil {
		t.Fatal("the first use did not compile the expression")
	}
	first := r.rx
	r.MatchString("b")
	if r.rx != first {
		t.Fatal("a later use compiled the expression again")
	}
}

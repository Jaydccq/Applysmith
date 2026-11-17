package match

import (
	"reflect"
	"regexp"
	"testing"
)

func TestEntryCapturedScope_InvalidItemsFailClosed(t *testing.T) {
	for name, scope := range map[string][]ScopeItem{
		"blank host":        {{Host: "  ", Question: "preferred language"}},
		"blank question":    {{Host: "jobs.example.test", Question: ""}},
		"every item broken": {{}, {Host: "jobs.example.test"}},
	} {
		e := abFixed("x", "A")
		e.CapturedScope = scope
		if !e.Scoped() {
			t.Errorf("%s: an entry that declares a scope stays scoped", name)
		}
		for _, host := range []string{"jobs.example.test", "", "other.example.test"} {
			if e.Admits(host, "preferred language", nil) || e.Admits(host, "", nil) {
				t.Errorf("%s: admitted on %q", name, host)
			}
		}
	}
	e := abFixed("x", "A")
	e.CapturedScope = []ScopeItem{{}, {Host: "jobs.example.test", Question: "preferred language"}}
	if !e.Admits("jobs.example.test", "preferred language", nil) {
		t.Error("a valid item still admits next to a broken one")
	}
}

func TestEntryStrayAskMe(t *testing.T) {
	among := Entry{Kind: KindPickOption, Body: Body{Preferred: []string{" " + AskMe, "Yes"}}}
	sole := Entry{Kind: KindPickOption, Body: Body{Preferred: []string{AskMe}}}
	multi := Entry{Kind: KindMultiSelect, Body: Body{Selected: []string{"a", AskMe}}}
	if !among.StrayAskMe() || !multi.StrayAskMe() || sole.StrayAskMe() || abFixed("f", AskMe).StrayAskMe() {
		t.Fatal("only a marker inside a longer list is stray")
	}
}

// fillValue sets every field reachable from v to a non-zero value, so a deep
// copy that forgets a field shows up as shared memory.
func fillValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(3)
	case reflect.Float64:
		v.SetFloat(0.8)
	case reflect.Ptr:
		if v.Type() == reflect.TypeOf(&regexp.Regexp{}) {
			v.Set(reflect.ValueOf(regexp.MustCompile("x")))
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillValue(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillValue(k)
		fillValue(e)
		v.SetMapIndex(k, e)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillValue(v.Field(i))
		}
	}
}

// sharedMemory returns the path of the first pointer, slice or map that a and
// b both hold (compiled patterns excepted: they are immutable).
func sharedMemory(a, b reflect.Value, path string) string {
	switch a.Kind() {
	case reflect.Ptr:
		if a.IsNil() || a.Type() == reflect.TypeOf(&regexp.Regexp{}) {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path
		}
		return sharedMemory(a.Elem(), b.Elem(), path)
	case reflect.Slice:
		if a.Len() == 0 {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path
		}
		for i := 0; i < a.Len(); i++ {
			if p := sharedMemory(a.Index(i), b.Index(i), path+"[]"); p != "" {
				return p
			}
		}
	case reflect.Map:
		if a.Len() == 0 {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path
		}
		for _, k := range a.MapKeys() {
			if p := sharedMemory(a.MapIndex(k), b.MapIndex(k), path+"{}"); p != "" {
				return p
			}
		}
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if p := sharedMemory(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); p != "" {
				return p
			}
		}
	}
	return ""
}

func TestBankClone_SharesNothingWithTheOriginal(t *testing.T) {
	var b Bank
	fillValue(reflect.ValueOf(&b).Elem())
	c := b.Clone()
	if !reflect.DeepEqual(b, c) {
		t.Fatal("the clone differs from the original")
	}
	if p := sharedMemory(reflect.ValueOf(b), reflect.ValueOf(c), "Bank"); p != "" {
		t.Fatalf("the clone shares memory with the original at %s", p)
	}
}

func TestBankClone_KeepsNilAndEmptyApart(t *testing.T) {
	b := Bank{Entries: []Entry{
		{ID: "a", Kind: KindMultiSelect, Body: Body{Selected: []string{}}},
		{ID: "b", Kind: KindMultiSelect},
	}}
	c := b.Clone()
	if c.Entries[0].Selected == nil || c.Entries[1].Selected != nil {
		t.Fatalf("an empty answer and an absent one must stay apart: %#v %#v", c.Entries[0].Selected, c.Entries[1].Selected)
	}
	if (Bank{}).Clone().Entries != nil || (Bank{}).Clone().Overrides != nil {
		t.Fatal("a nil list and a nil map stay nil")
	}
}

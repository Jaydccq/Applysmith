package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureDir = "../../../testdata/wire"

type manifestEntry struct {
	File      string `json:"file"`
	Type      string `json:"type"`
	Valid     bool   `json:"valid"`
	Canonical string `json:"canonical"`
}

// ContractProbe exercises the scalar rules of the wire for the fixtures that
// the Go and TypeScript tests share. Only the tests use it.
type ContractProbe struct {
	Count SafeInt `json:"count"`
	At    Time    `json:"at"`
}

func newFixtureValue(t *testing.T, name string) any {
	t.Helper()
	switch name {
	case "Failure":
		return &Failure{}
	case "SuccessHealth":
		return &Success[Health]{}
	case "ContractProbe":
		return &ContractProbe{}
	}
	typ, ok := Types[name]
	if !ok {
		t.Fatalf("fixture type %q is not in the wire registry", name)
	}
	return reflect.New(typ).Interface()
}

func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func compact(t *testing.T, b []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestFixturesRoundTripInGo(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []manifestEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Run(strings.TrimSuffix(e.File, ".json"), func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(fixtureDir, e.File))
			if err != nil {
				t.Fatal(err)
			}
			v := newFixtureValue(t, e.Type)
			err = strictDecode(in, v)
			if !e.Valid {
				if err == nil {
					t.Fatalf("%s decoded but the fixture is marked invalid", e.File)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := in
			if e.Canonical != "" {
				if want, err = os.ReadFile(filepath.Join(fixtureDir, e.Canonical)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if compact(t, got) != compact(t, want) {
				t.Fatalf("re-encoding differs\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestErrorCodesMapToTheirStatus(t *testing.T) {
	want := map[ErrorCode]int{
		CodeBadRequest: 400, CodeUnauthenticated: 401, CodeForbidden: 403, CodeNotFound: 404,
		CodeMethodNotAllowed: 405, CodeConflict: 409, CodeRevisionConflict: 409,
		CodePayloadTooLarge: 413, CodeUnsupportedMediaType: 415, CodeValidationFailed: 422,
		CodeConfigInvalid: 422, CodeRateLimited: 429, CodeInternal: 500, CodeUnavailable: 503,
	}
	if len(Codes()) != len(want) {
		t.Fatalf("closed set has %d codes, want %d", len(Codes()), len(want))
	}
	for c, s := range want {
		if c.Status() != s {
			t.Errorf("%s maps to %d, want %d", c, c.Status(), s)
		}
	}
}

func TestSafeIntRejectsValuesOutsideTheSafeRange(t *testing.T) {
	if _, err := json.Marshal(SafeInt(MaxSafeInt + 1)); err == nil {
		t.Error("encoding 2^53 succeeded")
	}
	if _, err := json.Marshal(SafeInt(-MaxSafeInt - 1)); err == nil {
		t.Error("encoding -2^53 succeeded")
	}
}

// Every exported field of a registered wire struct carries a json tag with a
// name, so the generated declarations cannot depend on Go field names.
func TestEveryWireFieldHasAJSONTag(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, where string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		custom := typ.Implements(reflect.TypeFor[json.Marshaler]())
		if typ.Kind() != reflect.Struct || custom || seen[typ] || typ.PkgPath() != reflect.TypeFor[Health]().PkgPath() {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				t.Errorf("%s.%s has no json tag name", where, f.Name)
			}
			walk(f.Type, where+"."+f.Name)
		}
	}
	for name, typ := range Types {
		walk(typ, name)
	}
	walk(reflect.TypeFor[ContractProbe](), "ContractProbe")
	walk(reflect.TypeFor[Failure](), "Failure")
	walk(reflect.TypeFor[Success[Health]](), "Success")
}

func TestEventPayloadsAreRegistered(t *testing.T) {
	for event, typ := range BrowserLiveEvents {
		if _, ok := Types[typ]; !ok {
			t.Errorf("live view event %s names unregistered payload %s", event, typ)
		}
	}
	for event, typ := range Events {
		if _, ok := Types[typ]; !ok {
			t.Errorf("event %s names unregistered payload %s", event, typ)
		}
	}
}

// Events names each event of the board and run streams and its payload type.
// The server publishes by name; these tables are what the tests compare the
// TypeScript unions and the registered types with.
var Events = map[string]string{
	"snapshot": "BoardSnapshot",
	"notice":   "BoardNotice",
	"reset":    "StreamReset",
	"shutdown": "StreamShutdown",
	"state":    "RunView",
	"phase":    "RunPhase",
	"progress": "RunProgress",
	"final":    "RunFinal",
	"chat":     "ChatEvent",
}

// NotificationEvents are the events only the notification stream carries. The
// stream also carries reset and shutdown.
var NotificationEvents = map[string]string{
	NotifyAnnounce: "NotificationAnnounce",
	NotifyWithdraw: "NotificationWithdraw",
}

// BrowserLiveEvents are the events of the live view stream.
var BrowserLiveEvents = map[string]string{
	"frame":       "BrowserLiveFrame",
	"tab":         "BrowserLiveTab",
	"cursor":      "BrowserLiveCursor",
	"filechooser": "BrowserLiveFileChooser",
	"end":         "BrowserLiveEnd",
}

// Codes lists the closed set in documentation order.
func Codes() []ErrorCode {
	return []ErrorCode{
		CodeBadRequest, CodeUnauthenticated, CodeForbidden, CodeNotFound, CodeMethodNotAllowed,
		CodeConflict, CodeRevisionConflict, CodePayloadTooLarge, CodeUnsupportedMediaType,
		CodeValidationFailed, CodeConfigInvalid, CodeRateLimited, CodeInternal, CodeUnavailable,
	}
}

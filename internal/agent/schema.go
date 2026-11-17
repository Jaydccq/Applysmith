package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// splitStep removes the reserved step argument from a call's arguments and
// returns the rest as written plus the raw phrase. Everything downstream (the
// schema check, the argument hash, the idempotency key and every gate) sees
// the arguments without it.
func splitStep(raw json.RawMessage) (args json.RawMessage, step string, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`), "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, "", fmt.Errorf("the arguments must be a JSON object")
	}
	if s, ok := obj[StepArg].(string); ok {
		step = s
	}
	delete(obj, StepArg)
	out, err := json.Marshal(obj)
	return out, step, err
}

// canonicalArgs re-encodes arguments with sorted keys and numbers as written,
// so two calls that differ only in key order or in step hash alike.
func canonicalArgs(args json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return string(args)
	}
	out, err := json.Marshal(v) // map keys are sorted by encoding/json
	if err != nil {
		return string(args)
	}
	return string(out)
}

// validateArgs checks arguments against the JSON Schema subset the catalog
// uses: object properties of type string, integer, number, boolean and array
// of those or of objects (which refuse an unlisted property only when they
// say additionalProperties false), enum, required, minimum, maximum, minLength,
// maxLength, minItems, maxItems and additionalProperties false. An argument the schema
// does not list is an error.
func validateArgs(schema map[string]any, args json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return fmt.Errorf("the arguments must be a JSON object")
	}
	props, _ := schema["properties"].(map[string]any)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub, ok := props[k].(map[string]any)
		if !ok {
			return fmt.Errorf("the argument %q is not in the schema", k)
		}
		if err := validateValue(k, sub, obj[k]); err != nil {
			return err
		}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if name, _ := r.(string); name != "" {
				if _, present := obj[name]; !present {
					return fmt.Errorf("the argument %q is required", name)
				}
			}
		}
	}
	return nil
}

func validateValue(path string, sch map[string]any, v any) error {
	if enum, ok := sch["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || e == v
		}
		if !found {
			return fmt.Errorf("%s must be one of %s", path, enumList(enum))
		}
	}
	switch sch["type"] {
	case "string":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if n, ok := sch["maxLength"].(float64); ok && utf8.RuneCountInString(s) > int(n) {
			return fmt.Errorf("%s is longer than %d characters", path, int(n))
		}
		if n, ok := sch["minLength"].(float64); ok && utf8.RuneCountInString(s) < int(n) {
			return fmt.Errorf("%s is shorter than %d characters", path, int(n))
		}
	case "integer", "number":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be a number", path)
		}
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) {
			return fmt.Errorf("%s is not a finite number", path)
		}
		if sch["type"] == "integer" && f != math.Trunc(f) {
			return fmt.Errorf("%s must be a whole number", path)
		}
		if lo, ok := sch["minimum"].(float64); ok && f < lo {
			return fmt.Errorf("%s is below %v", path, lo)
		}
		if hi, ok := sch["maximum"].(float64); ok && f > hi {
			return fmt.Errorf("%s is above %v", path, hi)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be true or false", path)
		}
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		props, _ := sch["properties"].(map[string]any)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub, ok := props[k].(map[string]any)
			if !ok {
				// A nested object that closes itself refuses what it does not list.
				if sch["additionalProperties"] == false {
					return fmt.Errorf("%s.%s is not in the schema", path, k)
				}
				continue
			}
			if err := validateValue(path+"."+k, sub, obj[k]); err != nil {
				return err
			}
		}
		if req, ok := sch["required"].([]any); ok {
			for _, r := range req {
				if name, _ := r.(string); name != "" {
					if _, present := obj[name]; !present {
						return fmt.Errorf("%s.%s is required", path, name)
					}
				}
			}
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be a list", path)
		}
		if n, ok := sch["minItems"].(float64); ok && len(list) < int(n) {
			return fmt.Errorf("%s holds fewer than %d items", path, int(n))
		}
		if n, ok := sch["maxItems"].(float64); ok && len(list) > int(n) {
			return fmt.Errorf("%s holds more than %d items", path, int(n))
		}
		if item, ok := sch["items"].(map[string]any); ok {
			for i, e := range list {
				if err := validateValue(fmt.Sprintf("%s[%d]", path, i), item, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func enumList(enum []any) string {
	parts := make([]string, len(enum))
	for i, e := range enum {
		parts[i] = fmt.Sprint(e)
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

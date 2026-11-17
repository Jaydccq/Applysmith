package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// The validator covers the JSON Schema subset that structured-output calls use.
// A schema with any other keyword is refused up front, so a constraint is never
// silently ignored.
var (
	annotationKeywords = map[string]bool{"$schema": true, "$id": true, "title": true, "description": true,
		"default": true, "examples": true, "format": true}
	supportedKeywords = map[string]bool{"type": true, "enum": true, "const": true, "properties": true, "required": true,
		"additionalProperties": true, "items": true, "minItems": true, "maxItems": true, "minLength": true,
		"maxLength": true, "minimum": true, "maximum": true, "pattern": true, "anyOf": true, "oneOf": true, "allOf": true}
)

// CheckSchema reports whether Structured takes schema, for a fake model in a
// test to refuse what the real one would.
func CheckSchema(schema json.RawMessage) error {
	_, err := checkSchema(schema)
	return err
}

// checkSchema decodes a schema and rejects unsupported keywords.
func checkSchema(raw json.RawMessage) (any, error) {
	var s any
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	return s, checkKeywords(s, "$")
}

func checkKeywords(s any, path string) error {
	m, ok := s.(map[string]any)
	if !ok {
		if _, isBool := s.(bool); isBool {
			return nil
		}
		return fmt.Errorf("schema at %s must be an object", path)
	}
	for k, v := range m {
		switch {
		case annotationKeywords[k]:
		case !supportedKeywords[k]:
			return fmt.Errorf("schema keyword %q at %s is not supported", k, path)
		}
		switch k {
		case "pattern":
			if p, isStr := v.(string); isStr {
				if _, err := regexp.Compile(p); err != nil {
					return fmt.Errorf("schema pattern at %s does not compile: %w", path, err)
				}
			}
		case "properties":
			props, _ := v.(map[string]any)
			for name, sub := range props {
				if err := checkKeywords(sub, path+"."+name); err != nil {
					return err
				}
			}
		case "items":
			if err := checkKeywords(v, path+"."+k); err != nil {
				return err
			}
		case "additionalProperties":
			// Every provider's schema form closes each object, so a map
			// (additionalProperties as a schema) could never be answered.
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("schema at %s: additionalProperties must be true or false; a map is sent closed, so list its entries as an array of objects", path)
			}
		case "anyOf", "oneOf", "allOf":
			list, _ := v.([]any)
			for i, sub := range list {
				if err := checkKeywords(sub, fmt.Sprintf("%s.%s[%d]", path, k, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validate appends a message per violation of schema by value.
func validate(schema, value any, path string, errs *[]string) {
	m, ok := schema.(map[string]any)
	if !ok {
		if b, isBool := schema.(bool); isBool && !b {
			*errs = append(*errs, path+": no value is allowed here")
		}
		return
	}
	add := func(format string, args ...any) { *errs = append(*errs, path+": "+fmt.Sprintf(format, args...)) }

	if t, ok := m["type"]; ok {
		types := []any{t}
		if list, isList := t.([]any); isList {
			types = list
		}
		matched := false
		for _, tt := range types {
			if typeMatches(fmt.Sprint(tt), value) {
				matched = true
			}
		}
		if !matched {
			add("expected %v, got %s", t, typeName(value))
			return
		}
	}
	if e, ok := m["enum"].([]any); ok {
		found := false
		for _, c := range e {
			if jsonEqual(c, value) {
				found = true
			}
		}
		if !found {
			add("is not one of the allowed values")
		}
	}
	if c, ok := m["const"]; ok && !jsonEqual(c, value) {
		add("must equal the constant value")
	}
	switch v := value.(type) {
	case map[string]any:
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				if _, has := v[fmt.Sprint(r)]; !has {
					add("missing required property %q", r)
				}
			}
		}
		props, _ := m["properties"].(map[string]any)
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := props[k]; ok {
				validate(sub, v[k], path+"."+k, errs)
				continue
			}
			if m["additionalProperties"] == false {
				add("property %q is not allowed", k)
			}
		}
	case []any:
		if n, ok := num(m["minItems"]); ok && float64(len(v)) < n {
			add("needs at least %v items", n)
		}
		if n, ok := num(m["maxItems"]); ok && float64(len(v)) > n {
			add("allows at most %v items", n)
		}
		if items, ok := m["items"]; ok {
			for i, e := range v {
				validate(items, e, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case string:
		n := float64(len([]rune(v)))
		if l, ok := num(m["minLength"]); ok && n < l {
			add("is shorter than %v characters", l)
		}
		if l, ok := num(m["maxLength"]); ok && n > l {
			add("is longer than %v characters", l)
		}
		if p, ok := m["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(v) {
				add("does not match the pattern %q", p)
			}
		}
	case float64:
		if l, ok := num(m["minimum"]); ok && v < l {
			add("is below the minimum %v", l)
		}
		if l, ok := num(m["maximum"]); ok && v > l {
			add("is above the maximum %v", l)
		}
	}
	for _, kw := range []string{"anyOf", "oneOf", "allOf"} {
		subs, ok := m[kw].([]any)
		if !ok {
			continue
		}
		matches := 0
		for _, sub := range subs {
			var e []string
			validate(sub, value, path, &e)
			if len(e) == 0 {
				matches++
			}
		}
		switch {
		case kw == "anyOf" && matches == 0:
			add("matches none of the alternatives")
		case kw == "oneOf" && matches != 1:
			add("must match exactly one alternative, matches %d", matches)
		case kw == "allOf" && matches != len(subs):
			add("must match every alternative")
		}
	}
}

func num(v any) (float64, bool) { f, ok := v.(float64); return f, ok }

func typeMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	}
	return false
}

func typeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return "number"
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// extractJSON strips code fences and returns the first balanced JSON object or
// array in s.
func extractJSON(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = rest
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, fmt.Errorf("the reply holds no JSON value")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return json.RawMessage(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("the reply holds an unterminated JSON value")
}

// promptWithSchema adds the schema to the prompt for a provider without
// schema-constrained output.
func promptWithSchema(prompt string, schema json.RawMessage) string {
	return prompt + "\n\nReply with a single JSON value that validates against this JSON Schema, and nothing else:\n" + string(schema)
}

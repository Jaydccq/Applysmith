package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The Messages API accepts only a subset of JSON Schema in
// output_config.format and answers 400 to the rest, so the schema that is sent
// is a reduced copy. The full schema stays with the caller, which validates the
// reply against it, so a constraint dropped here is still enforced.
// Source: platform.claude.com/docs/en/build-with-claude/structured-outputs.

// anthropicStripped are keywords the API rejects. Their values are folded into
// the description so the model still sees them.
var anthropicStripped = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "maxItems", "pattern"}

// anthropicFormats are the string formats the API accepts.
var anthropicFormats = map[string]bool{"date-time": true, "time": true, "date": true, "duration": true, "email": true,
	"hostname": true, "uri": true, "ipv4": true, "ipv6": true, "uuid": true}

// anthropicSchema returns the schema in the form the Messages API accepts:
// every object closed with additionalProperties:false, oneOf as anyOf, array
// bounds beyond minItems 0 or 1 and every numeric and string bound removed.
func anthropicSchema(raw json.RawMessage) (json.RawMessage, error) {
	var s any
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	return json.Marshal(reduceSchema(s))
}

func reduceSchema(node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	var dropped []string
	for _, k := range anthropicStripped {
		if v, has := out[k]; has {
			dropped = append(dropped, fmt.Sprintf("%s %v", k, v))
			delete(out, k)
		}
	}
	if n, has := out["minItems"]; has && n != float64(0) && n != float64(1) {
		dropped = append(dropped, fmt.Sprintf("minItems %v", n))
		delete(out, "minItems")
	}
	if f, has := out["format"].(string); has && !anthropicFormats[f] {
		delete(out, "format")
	}
	if one, has := out["oneOf"]; has {
		delete(out, "oneOf")
		out["anyOf"] = one
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		hint := "Constraints: " + strings.Join(dropped, ", ") + "."
		if d, _ := out["description"].(string); d != "" {
			hint = d + " " + hint
		}
		out["description"] = hint
	}
	if props, ok := out["properties"].(map[string]any); ok {
		reduced := make(map[string]any, len(props))
		for name, sub := range props {
			reduced[name] = reduceSchema(sub)
		}
		out["properties"] = reduced
	}
	if items, has := out["items"]; has {
		out["items"] = reduceSchema(items)
	}
	for _, kw := range []string{"anyOf", "allOf"} {
		if list, ok := out[kw].([]any); ok {
			reduced := make([]any, len(list))
			for i, sub := range list {
				reduced[i] = reduceSchema(sub)
			}
			out[kw] = reduced
		}
	}
	if _, hasProps := out["properties"]; hasProps || out["type"] == "object" {
		out["additionalProperties"] = false
	}
	return out
}

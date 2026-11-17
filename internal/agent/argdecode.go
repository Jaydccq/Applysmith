package agent

import (
	"encoding/json"
	"strings"
)

// decodeArgs reads validated arguments into a struct. Field names are matched
// ignoring case, so the structs of the tools stay short.
func decodeArgs(raw json.RawMessage, v any) error {
	return json.Unmarshal([]byte(strings.TrimSpace(string(raw))), v)
}

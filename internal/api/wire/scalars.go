package wire

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// MaxSafeInt is the largest integer a JavaScript number holds exactly.
const MaxSafeInt = 1<<53 - 1

// SafeInt is an integer that stays inside the range both languages represent
// exactly. Encoding or decoding a value outside it is an error, never a
// silent rounding.
type SafeInt int64

func (n SafeInt) MarshalJSON() ([]byte, error) {
	if n > MaxSafeInt || n < -MaxSafeInt {
		return nil, fmt.Errorf("wire: %d is outside the safe integer range", int64(n))
	}
	return strconv.AppendInt(nil, int64(n), 10), nil
}

func (n *SafeInt) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("wire: %q is not a whole number", b)
	}
	if v > MaxSafeInt || v < -MaxSafeInt {
		return fmt.Errorf("wire: %d is outside the safe integer range", v)
	}
	*n = SafeInt(v)
	return nil
}

const timeLayout = "2006-01-02T15:04:05.000Z"

// Time is an instant written as UTC with millisecond precision. Decoding
// requires an explicit zone designator, so a bare local time is rejected, and
// normalizes any offset to UTC.
type Time struct{ time.Time }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(timeLayout))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("wire: timestamp must be a string")
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("wire: %q is not an RFC 3339 timestamp with a zone", s)
	}
	t.Time = parsed.UTC()
	return nil
}

package gmail

import (
	"context"
	"time"
)

// Alert is an instant alert about one message. InterviewStart is the parsed
// start time when the message is an interview invitation; the notification
// policy treats an alert as urgent only when that time is within 24 hours.
type Alert struct {
	MessageID      string
	Title          string
	Body           string
	Link           string
	InterviewStart time.Time
}

// AlertSink delivers an instant alert. The server's sink goes through the
// notification policy, which decides urgency, quiet hours, de-duplication and
// the channels; this package only says what happened.
type AlertSink func(ctx context.Context, a Alert) error

// RaiseAlert hands an alert to the sink. A stack without a sink drops it.
func (s *Stack) RaiseAlert(ctx context.Context, a Alert) error {
	if s.Alerts == nil {
		return nil
	}
	return s.Alerts(ctx, a)
}

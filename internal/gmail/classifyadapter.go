package gmail

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"applysmith/internal/gmail/classify"
)

// SendersFile is the name of the user's sender-taxonomy override in the
// workspace.
const SendersFile = "gmail-senders.yml"

// LoadTaxonomy reads the override file at path and merges it over the shipped
// defaults. A missing file is not an error. A malformed one is returned as an
// error together with the defaults, so sync keeps running and the doctor can
// report the file.
func LoadTaxonomy(path string) (*classify.Taxonomy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return classify.Defaults(), nil
		}
		return classify.Defaults(), err
	}
	return classify.WithOverride(data)
}

// SignupWindowSource supplies the sign-up windows of the product's own apply
// runs: the runs that created an ATS account and the span in which mail from
// that ATS is the product's own doing. The apply chain (task 4.4) implements it
// over its run records. It is called for every message, so an implementation
// should be cheap. With no source the classifier cannot tell that mail from an
// employer's and keeps account-lifecycle mail out of the actionable tier.
type SignupWindowSource interface {
	SignupWindows() []classify.SignupWindow
}

// NewClassifyFunc adapts the classifier to the sync pipeline.
func NewClassifyFunc(tax *classify.Taxonomy, windows SignupWindowSource) ClassifyFunc {
	return func(m *Message, mailbox string) Verdict {
		c := &classify.Classifier{Tax: tax}
		if windows != nil {
			c.Windows, c.WindowsKnown = windows.SignupWindows(), true
		}
		res := c.Classify(classify.Input{
			MessageID: m.ID, ThreadID: m.ThreadID, From: m.From, To: m.To, Cc: m.Cc, Subject: m.Subject,
			Labels: m.Labels, Snippet: m.Snippet, Text: m.Text, Raw: m.RawText(),
			ReceivedAt: time.UnixMilli(m.InternalMS).UTC(), Mailbox: mailbox,
		})
		if res.Signal == nil {
			return Verdict{Kind: VerdictIgnored, Reason: res.Reason}
		}
		s := res.Signal
		v := Verdict{
			Kind: VerdictSignal, Type: s.Type, Tier: s.Tier, Direction: s.Direction,
			Employer: s.Company, Role: s.Role, Confidence: s.Confidence, RuleID: s.Evidence.Event.RuleID,
			ReviewReasons: s.ReviewReasons, Snippet: s.Snippet,
		}
		if !s.DueAt.IsZero() {
			v.DueMS = s.DueAt.UnixMilli()
		}
		detail, _ := json.Marshal(s.Evidence)
		v.DetailJSON = string(detail)
		// Bodies are kept for reclassification of mail that matters; mail a
		// sender restriction ruled out leaves only its snippet.
		if s.Tier != classify.TierSuppressed {
			v.Body, v.Raw = m.Text, m.RawText()
		}
		return v
	}
}

// RawText is the message's parts with markup intact.
func (m *Message) RawText() string {
	if m.Plain == "" {
		return m.HTML
	}
	if m.HTML == "" {
		return m.Plain
	}
	return strings.Join([]string{m.Plain, m.HTML}, "\n")
}

package gmail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"golang.org/x/text/encoding/htmlindex"

	"applysmith/internal/gmail/extract"
)

// Message is one Gmail message with its text parts decoded.
type Message struct {
	// ID and ThreadID are Gmail's own ids, the hexadecimal X-GM-MSGID and
	// X-GM-THRID, so they match the ids Gmail's web pages use.
	ID         string
	ThreadID   string
	InternalMS int64
	// Labels are Gmail label ids: INBOX, SENT and the other system labels by
	// their API names, a user label by its name.
	Labels  []string
	Snippet string

	From, To, Cc, Subject, Date string

	// Text is the plain parts as sent plus the HTML parts with markup removed,
	// in message order. HTML and Plain are the raw parts: an href does not
	// survive tag stripping.
	Text  string
	HTML  string
	Plain string
}

// maxPartDepth bounds the walk of a hostile multipart tree.
const maxPartDepth = 20

// snippetChars is the length of the snippet cut from the text.
const snippetChars = 200

// parse fills the headers and the text parts from a raw RFC 822 message. The
// message may be cut short (a fetch reads at most maxMessageBytes): what
// parsed before the cut is kept.
func (m *Message) parse(raw []byte) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return
	}
	h := msg.Header
	m.From, m.To, m.Cc = decodeHeader(h.Get("From")), decodeHeader(h.Get("To")), decodeHeader(h.Get("Cc"))
	m.Subject, m.Date = decodeHeader(h.Get("Subject")), h.Get("Date")
	var text, html, plain []string
	walkPart(textproto.MIMEHeader(h), msg.Body, 0, func(isHTML bool, s string) {
		if isHTML {
			html = append(html, s)
			text = append(text, extract.StripHTML(s))
			return
		}
		plain = append(plain, s)
		text = append(text, s)
	})
	m.Text = strings.Join(text, "\n")
	m.HTML = strings.Join(html, "\n")
	m.Plain = strings.Join(plain, "\n")
	m.Snippet = snippetOf(m.Text)
}

// walkPart emits the text/plain and text/html parts of a part tree, decoded
// to UTF-8. Attachments are skipped.
func walkPart(h textproto.MIMEHeader, body io.Reader, depth int, emit func(isHTML bool, s string)) {
	if depth > maxPartDepth {
		return
	}
	if d, _, _ := mime.ParseMediaType(h.Get("Content-Disposition")); d == "attachment" {
		return
	}
	mt, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
	if mt == "" {
		mt = "text/plain"
	}
	switch {
	case strings.HasPrefix(mt, "multipart/"):
		if params["boundary"] == "" {
			return
		}
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return
			}
			walkPart(p.Header, p, depth+1, emit)
		}
	case mt == "text/plain", mt == "text/html":
		data, _ := io.ReadAll(transferDecoder(h.Get("Content-Transfer-Encoding"), body))
		if len(data) > 0 {
			emit(mt == "text/html", toUTF8(params["charset"], data))
		}
	}
}

func transferDecoder(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// toUTF8 decodes a part in its declared charset; an unknown charset is read
// as UTF-8, with invalid bytes replaced.
func toUTF8(charset string, data []byte) string {
	if cs := strings.ToLower(strings.TrimSpace(charset)); cs != "" && cs != "utf-8" && cs != "us-ascii" {
		if enc, err := htmlindex.Get(cs); err == nil {
			if out, err := enc.NewDecoder().Bytes(data); err == nil {
				data = out
			}
		}
	}
	return strings.ToValidUTF8(string(data), "�")
}

var wordDecoder = mime.WordDecoder{CharsetReader: func(charset string, r io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(strings.ToLower(charset))
	if err != nil {
		return nil, err
	}
	return enc.NewDecoder().Reader(r), nil
}}

// decodeHeader decodes RFC 2047 encoded words; a header that does not decode
// is kept as sent.
func decodeHeader(v string) string {
	out, err := wordDecoder.DecodeHeader(v)
	if err != nil {
		return v
	}
	return out
}

// snippetOf is the start of the text on one line, as Gmail's snippet is.
func snippetOf(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if r := []rune(s); len(r) > snippetChars {
		return string(r[:snippetChars])
	}
	return s
}

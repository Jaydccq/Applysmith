package gmail

import (
	"errors"
	"strings"

	"applysmith/internal/lazyregexp"
)

// ConnectCommand stores the mailbox address and its app password; every
// message that names it can be run as written, the address filled in.
const ConnectCommand = "applysmith gmail connect --address <address> --password-stdin"

var (
	// ErrNotConnected means no app password is stored.
	ErrNotConnected = errors.New("Gmail is not connected; connect it with `" + ConnectCommand + "` or in Settings, Gmail")
	// ErrNoAllMail is an account whose folders show no All Mail: Gmail's IMAP
	// settings hide it from IMAP.
	ErrNoAllMail = errors.New("Gmail shows no All Mail folder over IMAP; in Gmail's settings, Labels, tick Show in IMAP for All Mail")
	// ErrAlreadyRunning refuses a second sync, resync or reclassify.
	ErrAlreadyRunning = errors.New("Gmail sync is already running")
	// ErrRefused is a command the read-only session refused before sending it.
	ErrRefused = errors.New("gmail: command refused by the read-only session")
)

// authFailureText is the sign-in vocabulary of the stored failure texts: a
// LoginError and ErrNotConnected both contain one of these.
var authFailureText = lazyregexp.New(`(?i)gmail refused the sign-in|gmail is not connected`)

// AuthFailureText reports whether a stored failure message is a sign-in
// failure: Gmail refused the app password, or none is stored.
func AuthFailureText(msg string) bool { return authFailureText.MatchString(msg) }

// SignInDetail is what the Gmail sign-in item says for a stored failure text:
// RefusalDetail for a refused app password, the text itself otherwise.
func SignInDetail(failure string) string {
	if strings.Contains(strings.ToLower(failure), "gmail refused the sign-in") {
		return RefusalDetail
	}
	return failure
}

// IsAuthFailure reports the same for an error value. Gmail's transient
// refusals (too many connections) and a connection that failed are not.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var le *LoginError
	return errors.Is(err, ErrNotConnected) || errors.As(err, &le)
}

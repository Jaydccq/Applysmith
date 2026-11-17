// Package classify turns one message into at most one typed signal. It reads
// only the message and a sender taxonomy and makes no network or storage call,
// so every rule is covered by table tests over synthetic mail.
//
// Event families are evaluated in a fixed precedence; the first family that
// matches, passes its guards and is permitted for the sender wins, and every
// family that matched and lost is recorded on the trace with the guard that
// blocked it.
package classify

// Version identifies the rule set. Decisions are keyed by message id and this
// version, so a change of rules re-decides prior mail as a new decision.
const Version = "gmail-classifier-go-5"

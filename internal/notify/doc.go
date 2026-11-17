// Package notify is the runs board, the needs-you inbox, the announcement
// policy and the desktop sink. The Telegram gateway is the sub-package
// telegram; it plugs into the policy through the Sink interface and into the
// board through Acker.
//
// Every producer writes its entry through Board. The board tells an Observer
// (the Policy) what changed, and the Policy decides what is announced, when,
// and to which sink: quiet hours, the burst limit, the delay of finished runs,
// de-duplication and withdrawal are all decided there and nowhere else.
package notify

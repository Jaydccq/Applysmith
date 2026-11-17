// Package gmail reads the user's own mailbox, read-only, to confirm
// applications, hand the apply chain a verification code or an account link,
// and keep a typed signal trail. It never sends, labels, deletes or drafts.
//
// It signs in to Gmail's IMAP server with the mailbox address and a Google
// app password; the password lives in the keychain through the vault, and
// everything else Gmail-derived lives in the workspace database. The classifier and the text extractors are separate
// packages (classify, extract) so they stay free of network and storage; the
// thread lifecycle and the link score are pure packages (lifecycle, link).
// What the product does with the stored signals, from tracker outcomes to the
// digests, is described in docs/architecture/gmail.md.
package gmail

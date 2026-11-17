// Package extract reads text out of mail: verification codes, account links,
// sign-in links to display and application receipts. It is pure so that every
// rule can be table-tested, and every extractor fails closed: when the message
// does not make the answer unambiguous it returns nothing.
package extract

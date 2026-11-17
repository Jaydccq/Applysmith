// Package signals runs the public hiring-signal pipeline inside the main
// process and the main database: collectors emit documents, ingest keys them,
// companies and events are identified and merged, a model assesses each new
// event under a fixed rubric, a versioned policy scores and tiers companies,
// admitted companies get their postings checked, qualifying postings are handed
// to the inbox and the leads store, and the top tier reaches the notification
// policy. One pass of that pipeline is a tick.
//
// The package reaches the outside world only through Deps: the HTTP client,
// the model, the key lookup, the lead sink and the notifier. Nothing here
// stores a key, a token or the contact email in the database.
package signals

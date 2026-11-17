// Package scheduler runs the user's schedules and the built-in timers inside
// the server process that holds the workspace lock.
//
// A schedule is a row in SQLite: a job kind, its inputs, a preset (daily,
// weekly or an RFC 5545 rule), a missed-fire policy and an enabled flag. Every
// tick re-reads the enabled rows, decides each due occurrence exactly once
// (a run row keyed by schedule and scheduled instant is inserted in the same
// transaction that advances last_fired_at), checks admission, takes the
// resource leases the kind declares and runs the job in its own goroutine.
//
// The job kinds are adapters over services that exist elsewhere; this package
// holds no scan, sync or evaluation logic. No kind can submit an application:
// registration refuses it and the dependency test pins the imports.
package scheduler

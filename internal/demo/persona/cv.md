# Alex Example

Durham, NC | alex@example.com | +1 919 555 0142 | https://alex.example.org
https://www.linkedin.com/in/alex-example-applysmith-demo | https://github.example.org/alex-example

## Summary

Platform and backend engineer with nine years of experience building
deployment platforms and payment services in Go. Builds internal platforms that
product teams use without a ticket, and keeps the on-call load small by
measuring it. Has mentored engineers but not managed them.

## Experience

### Senior Platform Engineer, Northwind Labs, Durham, NC (2022-04 to present)

- Replaced a hand-run release process with a self-service deployment pipeline;
  forty product teams now ship on their own and median lead time fell from two
  days to forty minutes.
- Moved 120 services from virtual machines to Kubernetes with no
  customer-visible outage.
- Cut the platform's cloud bill by 23 percent through right-sizing and
  scheduled scale-down of test environments.
- Led the weekly incident review and mentored four engineers; pages per
  on-call week fell from 31 to 9.

### Backend Engineer, Contoso Cloud, Raleigh, NC (2019-08 to 2022-03)

- Built the order and billing services in Go on PostgreSQL, handling 2 million
  orders a month.
- Wrote the idempotent payment retry that ended duplicate charges.
- Cut p99 checkout latency from 1.8 seconds to 420 milliseconds by batching
  inventory calls and reading from a replica.
- Added contract tests across 14 services, which caught 60 breaking changes
  before release.

### Software Engineer, Fabrikam Robotics, Raleigh, NC (2017-07 to 2019-07)

- Wrote the telemetry ingest service for 3,000 warehouse robots in Python on
  PostgreSQL.
- Cut the nightly reporting job from six hours to 55 minutes by partitioning
  tables and dropping redundant scans.
- Built the on-call dashboard that the operations team still uses to triage
  robot faults.

## Projects

- deploykit: an open-source command line tool that renders Kubernetes manifests
  from one short service file; used by 15 teams at Northwind Labs.
  https://github.example.org/alex-example/deploykit
- retrybox: a Go library for idempotent retries with a PostgreSQL-backed
  ledger. https://github.example.org/alex-example/retrybox

## Education

### BSc Computer Science, Example University (2013-09 to 2017-05)

## Skills

- Languages: Go, Python, SQL
- Platform: Kubernetes, Terraform, Docker, observability, incident response
- Data: PostgreSQL, Redis, message queues

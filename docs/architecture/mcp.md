# MCP server

`applysmith mcp` is a Model Context Protocol server over standard input and
output, built on the official Go SDK (`github.com/modelcontextprotocol/go-sdk`,
pinned in `go.mod`; its LICENSE holds Apache-2.0 and MIT texts during an
upstream relicensing, recorded as Apache-2.0 in `third_party/NOTICE-dependencies.txt`).
The `mcp-server` spec is normative; this file records the choices it leaves open.

## Shape

`internal/mcpserver` (layer 80) imports only `internal/api/wire`, `internal/vault`
and `internal/workspace`. It cannot reach the database, the tracker, the apply
chain or the agent, and a test (`boundary_test.go`) fails when its `go list -deps`
gains any other product package or when a source file listens, writes files,
runs programs or builds a non-GET request.

- `client.go`: the only door to data. The HTTP client sits on a transport that
  refuses, before sending, any method but GET and any path outside
  `allowedRoutes` (health, candidacies, applications/lookup, apply/list,
  reports/{number}). Redirects are not followed. At most 4 calls run at once,
  a call takes at most 8 s, the dial 2 s. The only retry is one re-read of the
  token after the API answers `unauthenticated`.
- `locate.go`: the port comes from `<workspace>/.applysmith/server.lock`; a lock
  file that names none means no server (`server_not_running`), never a guessed
  default, because the original app's bridge holds 47319. Only the port is
  used; the host is always 127.0.0.1, because the lock file is writable by
  anyone with the workspace and the bearer token goes to that address. Before
  every call, the client reads the public health route without the token and
  requires the workspace's own `workspaceId` (`workspace.PathKey`): a stopped
  server leaves its lock document behind, and another program may hold that
  port now. The answer is not kept between calls, so a tool call is two
  requests and the token is never sent on the strength of an earlier check. The token is
  `APPLYSMITH_SECRET_API_TOKEN` or the workspace's keychain entry, read once and
  kept; an `unauthenticated` answer reads it again and retries the call once.
- `tools.go`, `results.go`: five tools. Each result type lists its fields; the
  API's answer is decoded and copied field by field, so a field the API adds
  later does not appear in MCP output (allowlist, tested with extra fields).
  Output over 100,000 bytes is shortened (report Markdown first, then list items
  from the end) with `truncated` and `originalBytes`, which in all three tools
  is the size of the JSON of the whole result before it was shortened.
  `apply_list_status` also sets `truncated`, without `originalBytes`, when the
  list holds more rows than it reads.
- `errors.go`: tool errors are JSON text `{code, message, remedy,
  retryAfterSeconds?}` with the closed code set. The SDK's own rejection of
  out-of-schema arguments is rewritten to `invalid_argument` by a receiving
  middleware, so a client sees one error shape.

## Apply list

`apply_list_status` reads `GET /api/apply/list?pageSize=100`. The route
returns the lanes (`apply-now`, `selective`, `marked-applied`, `awaiting`),
each with its total and its page of items, and `counts.states`, the rows of
the page per state (`idle`, `queued`, `running`, `needs-user`, `submitted`,
`skipped`, `failed`). The tool reports `lanes` (lane totals) and gives each
item of the first page its `state`, the `reason` of its newest attempt's
outcome and its `runId`. While a lane holds more rows than the pages read so
far, it reads the next page too, so `counts` is the sum of `counts.states`
over every page and covers the whole list, as `lanes` does; it computes no
state itself.

## Client setup

`applysmith mcp --print-config` prints `{"mcpServers":{"applysmith":{"command":<absolute
executable>,"args":["mcp"],"env"?}}}`; `env.APPLYSMITH_WORKSPACE` appears only when
the workspace is not the default. `applysmith mcp --check` and the doctor check
`runtime.mcp` run the same stages (workspace, lock file, token, health). A lock file
that names no port ends the check at once with "no server running for workspace
<dir>", because no port is ever guessed.

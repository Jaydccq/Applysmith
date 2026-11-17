# Remote access

Opt-in, off by default, named HTTPS tunnel only. LAN mode does not exist: every
listener the process opens is bound to a loopback address (the guard rule
`loopback-listener` and `TestRemote_PrimaryListenerNeverBindsAllInterfaces`
enforce it), and pairing codes and device tokens travel only over the tunnel's
TLS. The specification is `openspec/changes/migrate-to-go-mygo/specs/remote-access`.

## Configure the tunnel

Install `cloudflared`, create a named Cloudflare Tunnel with an HTTPS public
hostname, and point its ingress at `http://127.0.0.1:<tunnel_port>` (47321 by
default). Set `remote_access.tunnel.public_url` in `policy.yml`. Store the token
under the vault key `cloudflared-tunnel-token`, or provide the child process's
`APPLYSMITH_SECRET_CLOUDFLARED_TUNNEL_TOKEN` environment override. The supervisor
passes it through `TUNNEL_TOKEN`, never an argument. A missing token produces
`named_tunnel_required`; a missing or invalid HTTPS URL, including a
`trycloudflare.com` host, produces `public_url_required`. Neither starts a child.
A registered tunnel connection is required before status becomes `running`.

[Cloudflare Quick Tunnels do not support SSE](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/),
which the dashboard uses for notifications, chat and run progress. The product
therefore has no Quick Tunnel mode. An SSE connection starts with a flushed
`: connected` comment so a proxy receives body bytes immediately; the comment
does not make an unsupported transport suitable for streaming.

The 2026-10-16 real Quick Tunnel check passed pairing and route restrictions
but failed event delivery. Named-tunnel lifecycle, configuration refusal and
local streaming have regression tests; real named-tunnel event delivery still
requires an authorized hostname and token.

## Pieces

| Where | What |
|---|---|
| `internal/remote/qr` | A QR encoder (byte mode, level M, versions 1 to 20, 512 bytes). Pure and in-repo, so there is no dependency; tests check the capacity table, a published error-correction vector and the format word. |
| `internal/remote/tunnel` | The supervisor of the `cloudflared` child: binary discovery and its 5 second version check, named-tunnel arguments, required token and HTTPS public-host validation, registered-connection readiness, the 1, 2, 4, 8, 16, 30 second restart delays (reset after 60 s, `failed` after 20 in a row), stop with terminate then kill after 5 s, the atomic status file `.applysmith/tunnel.json` with a 10 s heartbeat, and orphan cleanup (a process is ended only if it is a live cloudflared that started when the status file says the child did, so a reused identifier is left alone). It never downloads or updates the executable. |
| `internal/remote` | The paired devices (table `device_sessions`, digests only, kept in memory as digests and compared in constant time), the pairing offers (in memory, 3 outstanding, 300 s, once), the audit (`remote_events`, migration `0036`, swept after 90 days), the conversations a device started (`remote_sessions`, migration `0037`) and the settings read from `policy.yml`. |
| `internal/api/remote_*.go` | The tunnel listener, the remote principal, the routes, the pairing page. |
| `internal/cli/remote.go` | `applysmith remote ...`. |
| `web/src/pages/settings/Remote.tsx`, `Shell.tsx` | Settings, Remote access, and the header indicator. |

## How a request is classified

The listener that accepted the connection decides, never a header. The primary
listener serves the dashboard session and the local bearer token. In tunnel mode
the server also opens `127.0.0.1:<remote_access.tunnel_port>` (default 47321);
the `cloudflared` child connects to it, so its peer is loopback, and every
request on it is remote. A policy that is not exactly `mode: tunnel` reads as
off, so an unreadable or invalid `policy.yml` closes the listener. A change of
`policy.yml` is applied within 2 seconds (the server reads it every 2 s); the
tunnel listener closes, the child stops and every remote stream ends when the
mode leaves tunnel.

The primary listener refuses anything Cloudflare marked (a `Cf-` header or a
`Cdn-Loop` naming cloudflare) with `forbidden`, reason `tunnel_ingress`. A named
tunnel is configured in the Cloudflare dashboard, so its **ingress must point at
`http://127.0.0.1:<tunnel_port>`** (47321 by default), never at the dashboard
port; with a Host override of the loopback address, internet traffic on the
dashboard port would otherwise look local.

On the tunnel listener:

- The `Host` must be the configured `remote_access.tunnel.public_url` host;
  until the tunnel runs no host is accepted.
- Only routes the table marks `remote: allowed` are served; any other route,
  including one that does not exist, answers `forbidden` with the reason
  `remote_forbidden`, before a credential is looked at.
- The credential is a device token, in the `applysmith_device` cookie
  (`HttpOnly`, `Secure`, `SameSite=Strict`) or an `Authorization: Bearer`
  header. The local token and a dashboard session are no credential there; a
  device token is none on the primary listener.
- A cookie-authenticated mutation needs an `Origin` equal to `https://<host>`.
- The source of a request is the tunnel's client-address header (only the
  tunnel child can reach the listener); on the primary listener headers are
  ignored. 20 bad credentials from one source within a minute block it for 10
  minutes (`rate_limited`, reason `blocked`).
- At most 16 streams on the listener, shared evenly: a device may hold
  `16 / paired devices` of them, between 1 and 4, so the shares never add up to
  more than 16 and every paired device can open one. When devices pair after the
  listener filled up, a device with no stream gets one by closing a stream of the
  device that holds the most (its client reconnects within its share). A stream
  checks, once registered, that its device is still paired, so a revoke cannot
  slip in between the credential check and the registration. JSON bodies stop at
  256 KiB; no upload route is remote-allowed.
- A refused credential counts toward the block on public routes too, but one
  token digest counts once per minute, and an invalid device cookie is removed
  in the response (`Max-Age=-1`), so a revoked phone is one failure and cannot
  lock out the other devices behind the same address.
- A device posts to and reads only the conversations it started (table
  `remote_sessions`); anything else answers `not_found`. The desktop sees all.
- A card held because outside content was read is not approved from a device,
  and a card remembers that its turn was remote (`agent_confirmations.remote`),
  so the call that runs on approval keeps the read and draft envelope.
- `GET /api/health` answers the status only.

## The allowlist

`internal/api/testdata/remote-routes.golden` lists the remote-allowed routes and
`TestRemote_RouteTableGoldenFile_NoSecretOrAdminRouteRemote` compares it with the
table. The table itself refuses to build when an admin, secret, session-only or
process route is remote-allowed. The set is the one in the specification plus
three routes it needs: `POST /api/agent/confirmations/{id}/approve` and
`DELETE /api/agent/approvals/{id}` (the local-api specification marks them
remote-allowed; a device approves once, never a tier 4 call and never for
longer), and `POST /api/remote/pair/claim`, the route the pairing page posts to.
The claim issues a credential but cannot carry the `secret` flag the table
forbids on a remote route; the one-time code is the proof, five attempts a
minute per source.

`TestRemoteRouteClassPrincipalMatrix` runs every route of the table against
five callers (no credential, a paired device, the local token and a dashboard
session sent to the tunnel, and a device token sent to the primary listener) and
checks the answer the table implies.

A remote chat message is the dashboard session's route with a different tool
set: `Turn.Remote` makes the turn's tools the read and draft classes only (the
guard, the offered catalog and the model's instructions agree), the intent
router is not used, and the local token still may not send a message.

## Pairing

`POST /api/remote/offers` (admin class: the dashboard session or the local
token) makes an offer only while the tunnel runs behind an `https` address. The
link is `<origin>/pair#<code>`; the code is the fragment, so a request line
never holds it. The response also carries the QR modules, which the dashboard
draws as SVG and the CLI prints as text. `GET /pair` is a static page whose
script is allowed by its hash; it reads the fragment, removes it from the
address bar and posts the code with a label to `POST /api/remote/pair/claim`,
which sets the cookie and answers the dashboard address on the configured origin.
Without a device credential, any page address on the tunnel (not an asset) is
the pairing page. Revoking all devices and switching the mode off end every
outstanding offer, so a link made before either pairs nothing after it.

## Not in this change

- LAN mode (deferred by the owner decision of 2026-10-11).

## Screenshots and hidden sections (task 6.8)

`GET /api/browser/lanes/{lane}/tabs/{tabId}/screenshot` is the one remote-allowed
route under `/api/browser/` (it is in the golden file). For a remote device the
handler answers `forbidden` with `remote_forbidden` while
`remote_access.allow_screenshots` is false (read live from the applied policy),
and `rate_limited` for a second still within two seconds of the first (a gate per
device, `internal/api/browser_lanes.go`). A loopback caller is not gated by the
switch. No input route is remote-allowed. `TestRemote_ScreenshotsFollowThePolicyAndAreOnePerTwoSeconds`
and `TestRemote_NoBrowserInputRouteAndScreenshotsOffByDefault` hold this.

The dashboard asks `GET /api/remote/me` before it draws Settings. A paired
device sees the page heading and one sentence, none of the sections, and its
page asks for none of their routes; the server refuses them in any case
(`TestRemoteRouteClassPrincipalMatrix` with `testdata/remote-routes.golden`).

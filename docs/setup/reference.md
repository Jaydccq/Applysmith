# Reference: secrets and environment variables

## Secrets

A secret is stored only in the macOS keychain, under the account
`<workspace id>:<name>`, where the workspace id is the random identifier kept in
`~/Applysmith/.applysmith/workspace-id`. No workspace file, database row, log or run
record holds a secret value. A host with no keychain can supply any secret
through the environment variable `APPLYSMITH_SECRET_<NAME>`: the secret name
uppercased, with every character that is not a letter or digit replaced by an
underscore (`twocaptcha-key` becomes `APPLYSMITH_SECRET_TWOCAPTCHA_KEY`). A
variable that is set wins over the keychain; an empty one is ignored.

| Name | Holds | Set by | Step | Required |
|---|---|---|---|---|
| `signin-default` | Shared default sign-in password | Settings, Sign-in credentials | [13](13-credentials.md) | no |
| `signin-alternate-<id>` | Alternate shared password; `<id>` is 12 random hex characters | Settings, Sign-in credentials | [13](13-credentials.md) | no |
| `signin-site-<host>` | Override password for one site | Settings, Sign-in credentials | [13](13-credentials.md) | no |
| `tenant-<ats>-<tenant>` | Password for one employer's account on an applicant-tracking system | An apply run, from the password that signed in or the account it created | [13](13-credentials.md) | no |
| `api-key-<provider>` | API key of a model, discovery, search or hiring-signal provider | `applysmith llm configure` / `applysmith llm keys set` | [9](09-llm.md) | no |
| `chatgpt-refresh` | Sign in with ChatGPT refresh token; it rotates on every refresh and only the server refreshes it | Settings, Models and providers / `applysmith llm signin chatgpt` | [9](09-llm.md) | no |
| `chatgpt-access` | Sign in with ChatGPT access token, valid for one hour | Settings, Models and providers / `applysmith llm signin chatgpt` | [9](09-llm.md) | no |
| `provider-plan-<provider>-<key|cookie|workspace>` | Optional plan-usage credential of a provider account (MiniMax, GLM Coding Plan, OpenCode Go); separate from its model API key | Settings, Models and providers | [9](09-llm.md) | no |
| `gmail-app-password` | Google app password that signs in to Gmail over IMAP | Settings, Gmail / `applysmith gmail connect --password-stdin` | [11](11-gmail.md) | no |
| `telegram-bot-token` | Telegram bot token | Settings, Telegram / `applysmith telegram set-token` | [12](12-telegram.md) | no |
| `twocaptcha-key` | Captcha solver key | Settings, Captcha solver / `applysmith secrets set twocaptcha-key` | [14](14-captcha.md) | no |
| `github-token` | GitHub token for knowledge sources | Wiki, GitHub repositories / `applysmith secrets set github-token` | none | no |
| `cloudflared-tunnel-token` | Required named-tunnel token when remote access is enabled | `applysmith secrets set cloudflared-tunnel-token` | none | no |
| `api-token` | Local API bearer token; created at first start, replaced by `applysmith token rotate` | the server | none | yes |

A provider key is `api-key-` plus the provider id, for example
`api-key-openrouter`; its override is `APPLYSMITH_SECRET_API_KEY_OPENROUTER`.

A secret is never accepted as a command argument. The commands that take one
read it from a hidden prompt or standard input.

### The keychain asks for permission

The first time Applysmith reads or writes the keychain, macOS may ask whether to
allow it. Choose Always Allow. A locally built copy that is signed ad hoc gets
a new identity every time it is rebuilt, so macOS asks again after each
rebuild; that does not happen with a release build that keeps one signing
identity. If access was denied, unlock the keychain (Keychain Access) and
retry. `applysmith doctor --only runtime.keychain` runs a probe that stores,
reads and removes one item.

## Environment variables

Each variable overrides a default and never overrides a command-line flag. An
invalid value stops startup and names the variable.

| Variable | Meaning | Default |
|---|---|---|
| `APPLYSMITH_WORKSPACE` | Workspace directory | the recorded choice, else `~/Applysmith` |
| `APPLYSMITH_PORT` | Port of the server's loopback listener, 1 to 65535 (0 picks a free one) | 47319 |
| `APPLYSMITH_LOG_LEVEL` | `debug`, `info`, `warn` or `error` | `info` |
| `APPLYSMITH_CHROME_BIN` | Path of the Google Chrome executable; when set it is the only candidate | the standard install location |
| `APPLYSMITH_SECRET_<NAME>` | A secret's value, see above | none |
| `APPLYSMITH_MODEL_<TASK>` | Pins one task to `provider/model`; the task id is uppercased with hyphens as underscores (`APPLYSMITH_MODEL_QUICK_SCREEN`) | the stored routing |
| `APPLYSMITH_PI_PATH` | Executable of the Ask Agent's runtime (`pi`) when it is not on `PATH` | `pi` on `PATH` |
| `APPLYSMITH_DOCUMENT_TOOL_PATH` | Directories searched for the document tools (TeX, Poppler, Python) instead of the usual search | the usual search |

Applysmith sets `APPLYSMITH_AGENT_URL`, `APPLYSMITH_AGENT_TOKEN` and, for a model row
that uses more than one API style, `APPLYSMITH_AGENT_ROUTE` itself for the Ask
Agent's runtime; do not set them.

The repository's own checks read these variables; they matter only to people
who build Applysmith: `APPLYSMITH_PII_TOKENS`, `APPLYSMITH_RETIRED_NAMES`,
`APPLYSMITH_SIMILARITY_REFERENCES`, `APPLYSMITH_GUARD_RELEASE` and
`APPLYSMITH_UPDATE_NOTICES` ([development.md](../development.md)).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | Error (for `doctor`: a required check failed) |
| 2 | Input rejected; nothing was written |
| 3 | A person must act; the output says what |
| 4 | Environment failure (for `doctor`: unable to run) |

### Optional provider plan monitoring

`provider-plan-<provider>-<kind>` stores a plan API key, legacy console Cookie
header or OpenCode workspace identifier in the vault. The exact combinations
are enforced by the vault registry: `minimax-global` and `minimax-cn` accept
`key`/`cookie`; `glm-zai` and `glm-cn` accept `key`; `opencode-go` accepts
`key`/`cookie`/`workspace`. Use Settings → Models and providers → Accounts, under the
service's Manage (Plan usage). These credentials are separate from inference `api-key-<provider>`
keys. They are never returned by the API; only an explicit usage refresh sends
them to that provider's fixed HTTPS endpoint.

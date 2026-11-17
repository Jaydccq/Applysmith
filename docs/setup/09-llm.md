# Step 9: Language-model provider

## What this step does

Applysmith uses a language model to evaluate postings, write documents and
answer free-text questions. This step connects one provider, with your own
account. Without a working provider nothing is evaluated, written or answered
by a model.

There are four ways to connect:

- A preset: a provider with an API key you create in your own account. The
  presets are `openrouter`, `deepseek`, `openai`, `anthropic`, `gemini`,
  `opencode-go`, `kimi-code`, `minimax`, `minimax-cn`, `zai`, `bigmodel` and
  `xai` (the setup presets of `internal/llm/presets.go`).
- A command-line tool you are already signed in to (`codex` or `claude`). These
  support plain completion only: the Ask Agent then runs its deterministic
  router only, and embeddings (knowledge search) are off.
- Your ChatGPT plan, with no key: choose ChatGPT plan on this step and press
  Sign in with ChatGPT, then Save and test, which routes every task to it. The
  plan's own usage limits apply and nothing is billed. Outside the wizard, sign
  in with `applysmith llm signin chatgpt` or Settings, Models, Accounts; that adds
  the provider and changes no route: name it on a task row
  (`applysmith llm set <task> chatgpt/<model>`), or route every task to it with
  `applysmith llm configure --preset chatgpt` (`{"preset": "chatgpt"}` below).
- Offline: no key yet. Every task is served by a fake provider and reports say
  so. Use it to look around, not to apply. The Ask Agent needs a real model, so
  it does not answer and Home offers no "Let the agent do it".

## Before you start

For a preset, create an API key on the provider's website. A key is a secret:
paste it only into the terminal prompt below, never into a chat, a file or a
command line.

## Steps

Connect a preset. The key is read from the hidden prompt (or from standard
input), never from an argument:

```
applysmith llm configure --preset openrouter --key-stdin
```

Or use a signed-in command-line tool:

```
applysmith llm configure --cli codex
```

Test the connection with one small call:

```
applysmith llm test
```

Then record the step:

```
applysmith setup apply llm --file llm.json
```

with `llm.json` holding one of:

```json
{"preset": "<preset name>", "fallback": "<optional second preset>"}
```

```json
{"cli": "codex", "acknowledge_cli_limits": true}
```

```json
{"offline": true}
```

Routing of individual tasks to individual models is optional:

```
applysmith llm show
applysmith llm set <task> <provider>/<model>
applysmith llm reset <task>
applysmith llm cost
```

These commands, `group`, `budget` and `cap` work while the app runs: the
change goes to the running app the way Settings, Models sends it.

A group spreads one task's calls over several models (mode `order`, `rotate`
or `usage`; see [llm.md](../architecture/llm.md)):

```
applysmith llm group set <name> --mode order|rotate|usage --members <provider>/<model>,<provider>/<model>
applysmith llm set <task> group/<name>
applysmith llm group rm <name>          # refused while a task is routed to it
```

Spend limits, in US dollars per local day (`applysmith llm show` lists them):

```
applysmith llm cap <usd>                # every task; 0 turns the cap off (default 2)
applysmith llm budget <task> <usd>      # one task; 0 removes its budget
```

At a limit, calls move to a cheaper model, and at the cap or an evaluation
task's budget, unattended evaluation waits for the next day.

## Where this is stored

The API key is in the macOS keychain (secret `api-key-<provider>`; a headless
host can use the `APPLYSMITH_SECRET_API_KEY_<PROVIDER>` environment variable, see
[reference.md](reference.md)). The provider choice, routing and the saved test
result (provider, model, outcome, latency, cost; never a prompt or a reply) are
in the workspace database.

## How to check it worked

```
applysmith doctor --only llm
```

```text
[PASS] llm.provider   1 provider(s) usable, every core task routed
[PASS] llm.test-age   last test of <provider>/<model> succeeded 3m0s ago
```

The step completes only when the latest test succeeded and the provider has
not changed since. A test older than seven days is a warning.

## If something goes wrong

| What you see | What to do |
|---|---|
| `no provider is configured` | Run `applysmith llm configure ...` above. |
| `rejected` or an authentication error on test | The key is wrong or revoked. Run the configure command again with a new key. |
| `no test call has run` / `changed after the last test` | Run `applysmith llm test`. |
| The keychain asks for permission | Allow access. If it was denied, unlock the keychain and retry; see [reference.md](reference.md) for the environment alternative. |
| Core tasks "without a route" | `applysmith llm show` lists them; route them with `applysmith llm set`. |

An agent following this page must not ask you for the key in chat: see
[for-agents.md](for-agents.md).

Next: [Step 10: apply window](10-browser.md).

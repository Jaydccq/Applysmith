# For coding agents helping with setup

You are helping a person set up Applysmith by following the pages of this
folder and running `applysmith` commands. These rules are not negotiable.

1. **Ask the user for every personal value.** Name, email, phone, address,
   work authorization, targets, CV text, narrative: all of it comes from the
   user in this conversation. Never invent a value, never copy one from an
   example, never reuse one from another project or from files you happen to
   see. Example values and reserved example domains are rejected on purpose.
2. **Show any derived value for confirmation** before you write it: a
   normalised phone number, a country code, a CV draft, a guessed answer-bank
   entry. The user confirms; you do not.
3. **Secrets go through standard input only.** API keys, passwords and bot
   tokens are never put on a command line, in a file in the workspace, in a
   log or in the chat; a Gmail app password is typed by the user at the hidden
   prompt of `applysmith gmail connect --address <a> --password-stdin`. A
   secret with no command of its own (the captcha key and the tokens in
   [reference.md](reference.md)) is stored with `applysmith secrets set <name>`,
   which reads it from a hidden prompt or standard input; for the captcha key
   that is `applysmith secrets set twocaptcha-key`. Ask the user to run the
   command that reads the secret from a hidden prompt themselves, or to paste
   it into the terminal prompt. Do not ask for the secret in chat.
4. **Steps that need a person stay with the person.** A command that exits 3
   needs a human: a Gmail app password, creating a Telegram bot, a
   2Captcha account, loading the extension. Print the instruction and wait.
5. **Never submit an application.** Setup does not need `applysmith apply
   --submit` and you must not run it. Submitting happens only on the user's
   explicit request for a named posting.
6. **Read before you write.** Run `applysmith setup status --json` and
   `applysmith setup next --json` first; run `applysmith setup schema <step>` for the
   fields; run every `applysmith setup apply ...` with `--dry-run` first; read the
   result and exit code.
7. **Do not edit workspace files by hand** to finish a step. The writers
   validate and keep comments and revisions; `policy.yml` is edited only by the
   user.

## Loop

```
applysmith setup next --json
applysmith setup schema <step>
applysmith setup apply <step> --file <path> --dry-run
applysmith setup apply <step> --file <path>
applysmith doctor --only <step>
```

Repeat until `applysmith setup status` shows every required step complete. For
the commands you may run beyond setup, and how to treat their output and exit
codes, see the product skill in `skills/applysmith/SKILL.md` of the repository.

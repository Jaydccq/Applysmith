# Setting up Applysmith

Applysmith is a local-first job-search tool for macOS. It runs on your own
computer, keeps your data in one folder you own, and talks to the outside
world only for the services you connect. This guide takes you from nothing to
a first evaluated posting. There is one page per step, in order.

**Supported platform:** macOS 13 or later. Other operating systems are
unverified. You also need Google Chrome 120 or later.

## Install

Open the disk image and drag Applysmith to the Applications folder. That is the
whole install. The `applysmith` command is not on your PATH yet. Open Applysmith,
go to Settings, Advanced, Command line and choose Install the command. It makes
one link, `~/.local/bin/applysmith`, to the program inside the app, and edits no
other file. If the page says `~/.local/bin` is not on your PATH, it shows the
one line to add to your shell profile (for zsh, `~/.zshrc`); add it yourself.
Then open a new Terminal window (press Command-Space, type Terminal, press
Return) and confirm the command answers:

```
applysmith --version
```

A different file already named `applysmith` in `~/.local/bin` is never replaced;
the page tells you so. Remove the link from the same place; the app stays.

Status: the disk image comes from the packaging work, which is not
finished. Until it ships, build from source as described in
[development.md](../development.md).

### The app is not notarized

Applysmith is signed ad hoc, not with an Apple Developer ID, and it is not
notarized by Apple. macOS therefore refuses the first launch of a copy you
downloaded, with a message that it cannot check the app for malicious
software. Allow this one app, once:

1. Open Applysmith from the Applications folder. When macOS refuses it, choose
   Done (not Move to Trash).
2. Open System Settings, then Privacy & Security, and scroll to Security.
3. Next to the message about Applysmith, choose Open Anyway, and confirm with
   your password or Touch ID.
4. In the dialog that follows, choose Open.

Later launches open normally. Never turn Gatekeeper off or change any other
system-wide security setting to run Applysmith: Open Anyway allows this app and
nothing else. A build you made yourself with `make package` opens on the Mac
that built it without these steps.

Secrets are kept in the macOS keychain through its `security` tool, not under
the app's own signature, so a new ad-hoc signature with every build does not
lock you out of them. Replacing the app with a newer build (two upgrades and a
downgrade were tried) raised no keychain prompt. If macOS ever asks to allow
keychain access, allow it. The measurements are in
[architecture/packaging.md](../architecture/packaging.md).

## See it working first: the demo

Before you enter anything of your own you can open the demo workspace: a
separate folder, `~/Applysmith-Demo`, with the fictional persona Alex Example,
three sample postings from fictional companies and a sample application form.
It never touches your own workspace: it never presses Submit, creates an
account, resets a password, solves a captcha, opens an activation link, sends
mail, or sends a Telegram message. It reads mail only to type a site's emailed
verification code, and only when you have connected your own Gmail account and
given the persona a plus alias of it (`applysmith demo email`, in the
[tutorial](../user-guide/tutorial.md#optional-let-the-demo-read-one-emailed-code)).
The only files it uploads are the persona's own fictional documents.

```
applysmith demo init
applysmith serve --workspace ~/Applysmith-Demo
```

The Setup page's "Try the demo" does the same. The
[tutorial](../user-guide/tutorial.md) walks through it.

## The steps

You can follow them in three ways: this guide, the interactive command
`applysmith setup`, or the setup page of the dashboard. All three read the same
list of steps, so they cannot disagree.

| Step | Page | Needed |
|---|---|---|
| 0 | [Check this computer](00-prerequisites.md) | yes |
| 1 | [Create the workspace](01-workspace.md) | yes |
| 2 | [Your CV](02-cv.md) | yes |
| 3 | [Your identity and contact details](03-identity.md) | yes |
| 4 | [Work authorization and location](04-work-authorization.md) | yes |
| 5 | [What you are looking for](05-targets.md) | yes |
| 6 | [Your story and voice](06-story.md) | yes |
| 7 | [Résumé PDFs](07-resumes.md) | yes |
| 8 | [Answer bank review](08-answer-bank.md) | yes |
| 9 | [Language-model provider](09-llm.md) | yes |
| 10 | [Apply window](10-browser.md) | yes |
| 11 | [Gmail](11-gmail.md) | optional |
| 12 | [Telegram](12-telegram.md) | optional |
| 13 | [Sign-in credentials](13-credentials.md) | optional |
| 14 | [Captcha solver](14-captcha.md) | optional |
| 15 | [Job sources and schedule](15-discovery.md) | optional |
| 16 | [Chrome extension](16-extension.md) | optional |
| 17 | [Orientation](17-orientation.md) | yes |

Progress is the finished required steps out of 12. Skipping an optional step
never lowers it, but each page says what you lose by skipping.

Also: [reference.md](reference.md) lists every secret and environment variable,
and [for-agents.md](for-agents.md) is for a coding agent that helps you.

## Where am I?

```
applysmith setup status
applysmith setup next
applysmith doctor
```

`setup status` lists every step with what is missing. `setup next` shows the
first incomplete required step with its commands. `doctor` runs every check
and prints what to fix; it exits 0 when everything required passes, 1 when a
required check fails and 4 when it cannot run.

## Conventions on these pages

- `~` is your home folder, and `~/Applysmith` is the default workspace.
- Values in angle brackets, like `<your email>`, are yours to fill in. Nothing
  in this guide is a real person's value.
- A command that changes something has a `--dry-run` that checks the input and
  writes nothing.
- Commands take `--workspace <folder>` when your workspace is not the default.
- Exit codes: 0 done, 1 error, 2 input rejected, 3 a person must act, 4 the
  environment failed.
- A secret (password, API key, token) is never typed as a command argument and
  never goes into a file in the workspace. It goes to the macOS keychain
  through a hidden prompt or standard input.

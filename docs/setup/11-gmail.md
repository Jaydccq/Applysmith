# Step 11: Gmail (optional)

## What this step does

It lets Applysmith read your job-related mail to track outcomes (interview
invitations, rejections) and to read verification codes during an
application. It is read-only: Applysmith signs in over IMAP, opens mail without
marking it read, and never sends, deletes, moves or labels it.

If you skip it, one-time codes, outcome tracking and digests are unavailable.
`applysmith setup skip gmail` records the decision.

## Before you start

You need a Google app password for the Gmail account (about 2 minutes, once).
An app password is a 16-letter password that Google issues for one app; it
reads mail over IMAP and cannot sign in to your Google Account on the web.

1. **Turn on 2-Step Verification.** Google offers app passwords only on an
   account with 2-Step Verification. Open
   <https://myaccount.google.com/signinoptions/twosv>, signed in as the Gmail
   account you want Applysmith to read, and turn it on if it is off.
2. **Create an app password.** Open <https://myaccount.google.com/apppasswords>
   (Google Account, Security, App passwords), enter any name (for example
   "Applysmith") and click **Create**. Google shows the password once, as four
   groups of four letters. Keep the page open until it is entered below; the
   spaces may be left in.

Some Google Workspace administrators turn app passwords off for their
organization. An account that shows no App passwords page cannot be connected.

Gmail's IMAP is on for every account. If Gmail's settings still show an IMAP
switch (**See all settings**, **Forwarding and POP/IMAP**), it must read
enabled; and under **Labels**, All Mail must keep **Show in IMAP** ticked
(the default), because Applysmith reads All Mail.

## Steps

In the dashboard (this step, or Settings, Gmail): enter the Gmail address and
the app password and press **Connect**. Applysmith signs in to Gmail with them
first and stores them only when Gmail accepts them; the page then shows the
connected address, and the running app reads your job mail from then on as it
arrives. **Test the connection** signs in again and counts the messages of the
last day. **Disconnect** removes the app password, stops the reading and pauses
any Gmail sync schedule you made until you connect again; the address and the
mail already read stay. Connecting another account than the
last one starts its sync afresh; the mail already read stays.

At a terminal, the password is read from standard input, never from an
argument. Type it at the hidden prompt:

```
applysmith gmail connect --address <your Gmail address> --password-stdin
applysmith gmail test
```

or pipe it, for example from a password manager's command line:

```
printf '%s' "$APP_PASSWORD" | applysmith gmail connect --address <your Gmail address> --password-stdin
```

While Applysmith runs, it keeps one connection to Gmail open and reads new job
mail within seconds of its arrival; there is no schedule to set. To read mail
now from the terminal and see the connection status:

```
applysmith gmail sync
applysmith gmail status
```

## Where this is stored

The app password is in the macOS keychain (`gmail-app-password`). The address
and the non-secret metadata (sync position, classified messages) are in the
workspace database.

## How to check it worked

```
applysmith doctor --only gmail
```

```text
[PASS] gmail.login    an app password is stored and Gmail has not refused it
```

`gmail.fetch` signs in and searches once; it runs only with
`applysmith doctor --live`.

## Gmail in the demo workspace

The demo workspace (`~/Applysmith-Demo`) can be connected too, with `--workspace
~/Applysmith-Demo` on `connect`, `test` and `status`. There it reads exactly one
kind of mail: a site's emailed verification code for the demo persona, when
the persona's address is a plus alias of the connected account (set with
`applysmith demo email`, see the [tutorial](../user-guide/tutorial.md#optional-let-the-demo-read-one-emailed-code)).
A sync, resync, reclassify, digest or scan is refused there, and `test` counts
messages without opening any.

## If something goes wrong

| What you see | What to do |
|---|---|
| `the keychain is not open` / `database is not open` | Complete [step 1](01-workspace.md) and start the server. |
| `a Google app password is 16 letters` | You entered the Google Account's own password. Create an app password (above) and enter that. |
| `Gmail refused the sign-in (Invalid credentials …)` when connecting | Check the address, and that the app password was created while signed in to that same account. Create a new one if in doubt. |
| `Gmail no longer accepts the app password` / **Gmail needs its app password** in the inbox | The app password was removed in your Google Account, or the account's password changed (which ends every app password). Create a new app password and connect again; the sync carries on where it stopped. |
| No **App passwords** page in your Google Account | 2-Step Verification is off, or a Google Workspace administrator turned app passwords off. Turn on 2-Step Verification; for a work account, ask the administrator. |
| `Gmail shows no All Mail folder over IMAP` | In Gmail's settings, **Labels**, tick **Show in IMAP** for All Mail. |
| `Gmail could not be reached` | A network failure or Gmail's "too many connections": nothing is wrong with the password, and the next sync tries again. |
| Stop reading this mailbox | `applysmith gmail disconnect` (or **Disconnect**) removes the app password, stops the reading and pauses any sync schedule you made; remove the app password in your Google Account too to end it there. |
| Wrong mail was classified | `applysmith gmail reclassify --yes` re-runs classification. `applysmith gmail resync --yes` re-reads, and `applysmith gmail purge --yes` deletes what Applysmith stored about your mail and the app password. |

Next: [Step 12: Telegram](12-telegram.md).

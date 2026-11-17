# Step 12: Telegram (optional)

## What this step does

It connects a Telegram bot of your own, so what you can act on away from the
desk reaches your phone (employer mail, interviews, the assistant's questions,
the daily briefs; runs you started in the dashboard stay on the desktop) and
you can chat with the Ask Agent from Telegram. Telegram can read and draft; it
can never start an application. If you skip it, notifications go to the
desktop only. `applysmith setup skip telegram` records the decision.

## Before you start

Create the bot in Telegram: open the chat with BotFather, send `/newbot`,
choose a display name and a username that ends in `bot`, and copy the token it
gives you. The token is a secret; paste it only where the command below asks.

## Steps

In the app, Settings, Telegram does both: paste the token into Bot token and
press Save token, then press Get a pairing code. Open the link it shows (or
scan its QR code with your phone) and press Start in Telegram: that sends the
code to your bot and pairs the chat. Sending the code to the bot yourself, from
a private chat, works too. Once paired, write to the bot as you would to a
person, in any language; the menu button beside the message box lists the
commands. At a terminal:

Store the token. It is read from standard input, checked with Telegram and
saved in the keychain:

```
applysmith telegram set-token --stdin
```

Pair your chat. This prints a one-time code, valid for 10 minutes, with a link
and a QR code that send it to your bot when you press Start; or send the code
yourself from a private chat:

```
applysmith telegram pair
```

```text
Open https://t.me/<your bot>?start=<code> and press Start, or scan this with your phone:
<QR code>
Or send this code to @<your bot> from a private chat within 10 minutes:
<code>
```

Check the result:

```
applysmith telegram status
```

To undo: `applysmith telegram unpair` ends the pairing, and
`applysmith telegram clear-token` removes the token.

## Where this is stored

The bot token is in the macOS keychain (`telegram-bot-token`; a headless host
can use `APPLYSMITH_SECRET_TELEGRAM_BOT_TOKEN`). The paired chat is in the
workspace database. Only that private chat is served; everyone else is ignored.

## How to check it worked

```
applysmith doctor --only telegram
```

```text
[PASS] telegram.token     a bot token is stored
[PASS] telegram.pairing   a chat is paired
```

Both lines only warn when missing; they never fail the doctor.

## If something goes wrong

| What you see | What to do |
|---|---|
| The token is refused | Copy the whole token from BotFather again. Create a new one with `/token` if it was revoked. |
| `the keychain is not available` | Complete step 1, or supply the token through the environment variable above. |
| The code is not accepted | It expires after 10 minutes and five wrong attempts close the window; run `applysmith telegram pair` again and send it from a private chat, not a group. |
| No message arrives | Check `applysmith telegram status`; quiet hours hold normal notifications until morning (see [the daily loop](../user-guide/README.md)). |

Next: [Step 13: sign-in credentials](13-credentials.md).

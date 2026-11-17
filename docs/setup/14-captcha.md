# Step 14: Captcha solver (optional)

## What this step does

A captcha is a test that asks "are you human". With your own account at the
2Captcha service, Applysmith can clear a supported captcha inside an apply run
that you started and that submits. If you skip it, every captcha is left to
you. `applysmith setup skip captcha` records the decision.

What it does and does not touch is fixed. It clears reCAPTCHA v2 (and
Enterprise), hCaptcha and a Cloudflare Turnstile box embedded in a page. It
never touches a full-page Cloudflare check, image puzzles, Arkose/FunCaptcha or
DataDome. It never runs during a scan, a dry run, a scheduled job or for an
employer on your batch skip list. At most 2 challenges are solved per run, and
it stops when the account balance is under USD 0.50. See
[policies](../architecture/policies.md).

## Before you start

Create an account at 2captcha.com, add a little balance, and copy its API key.
The key is a secret.

## Steps

In the app, open Settings, Captcha solver, paste the key into 2Captcha API key
and press Save key; it goes to the keychain. The command below writes nothing
while the app or `applysmith serve` holds the workspace.

Store the key in the keychain. Copy it to the clipboard first, so it never
appears in your shell history; the command reads it from standard input:

```
pbpaste | applysmith secrets set twocaptcha-key
```

Or supply it to the server through the environment and restart it:

```
export APPLYSMITH_SECRET_TWOCAPTCHA_KEY="$(pbpaste)"
applysmith serve
```

The kill switch is `captcha_solver` in `policy.yml`: `on` (the default) or
`off`. No environment variable switches the solver; with `off` a stored key is
kept but unused. In the app, Use the solver in Settings, Captcha solver
writes that line.

## Where this is stored

The key is the secret `twocaptcha-key`, read from the keychain or from the
`APPLYSMITH_SECRET_TWOCAPTCHA_KEY` variable (the variable wins). It is never
written to the workspace, a log or a run record. The switch is in `policy.yml`.

## How to check it worked

```
applysmith doctor --only captcha
```

The check reports whether a key is available. It makes no call to 2Captcha;
the balance is read only inside an apply run.

## If something goes wrong

| What you see | What to do |
|---|---|
| `captcha.key` warns the keychain is not open | Complete [step 1](01-workspace.md) and start the server, or supply the key through the environment variable. |
| A challenge is left to you in a run | Read the `captcha:` line in the run's stage detail: unsupported kind, `solver_off`, cap reached, low balance or the company is on your skip list. |
| `balance_low` | Add balance; the floor is USD 0.50. |
| The run stopped at a sign-up or an emailed code, not a captcha | Its item in Needs you links the fix: Settings, Applications for account creation (only you sign and set it) or Settings, Gmail for the mail. See [step 13](13-credentials.md). |
| You do not want it any more | Clear Use the solver in Settings, Captcha solver (`captcha_solver: off`), or delete the key. |

Next: [Step 15: job sources and schedule](15-discovery.md).

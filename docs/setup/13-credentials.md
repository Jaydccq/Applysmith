# Step 13: Sign-in credentials (optional)

## What this step does

Many application sites need an account. This step stores a shared default
sign-in (an email and a password) that Applysmith tries on those sites. When an
application you started needs an account and none is stored, the run creates
one with your profile email; nothing has to be signed first and there is no
weekly quota. A cooldown after a challenge, lockout or rate limit still holds
that site. Instead of a password, or as well, you can let runs use Sign in
with Google (below). The step is done when either is set. If you skip it,
signing in on application sites is left to you. `applysmith setup skip
credentials` records the decision.

## Before you start

Choose the password you want Applysmith to try first on application sites. It is
stored only in the keychain.

## Steps

1. Store the shared default sign-in in the dashboard: Settings, Sign-in
   credentials. Enter the email and the password there. The password field is
   write-only, and a shared default needs 12 characters with an uppercase
   letter, a lowercase letter, a number and a symbol. The same page holds the
   per-site overrides (any password the site accepts), the alternate passwords
   and their order.

   From a terminal, the password is read from standard input or a hidden
   prompt and is never accepted as an argument:

   ```
   applysmith credentials set-default --email you@example.com
   applysmith credentials set-default --password-stdin
   applysmith credentials add-alternate --label "Old laptop" --password-stdin
   applysmith credentials set-site careers.example.test --password-stdin
   applysmith credentials reorder <id> <id>
   applysmith credentials list
   ```

   `applysmith secrets list|set|delete|test` does the same for any registered
   secret. A test reports whether the secret can be read and from where; no
   kind has a live provider check yet.

2. Optional: Sign in with Google. In the same section, tick Automatically
   sign in, authorize and register with Google (off by default). First sign in
   to Google once in the apply window: the Browser page opens it, and the
   window keeps its own Chrome profile. A run then presses a site's Sign in
   with Google, uses that session, picks your profile email and approves only
   basic name, email and profile access; it may register a site account the
   same way. A new Google login, extra permissions and Google's own
   verification stop the run for you. Google cookies are never copied from
   your everyday Chrome.
Settings, Applications shows the cooldowns running. A run stopped by one links
its item in Needs you straight to that section.

Sign-in codes and activation or reset mails are read from Gmail. Connect it
in [step 11](11-gmail.md); a run that could not read such a mail links its
item to Settings, Gmail.

## Where this is stored

| What | Where |
|---|---|
| Passwords | The macOS keychain (`signin-default`, alternates `signin-alternate-<id>`, per-site `signin-site-<host>`, per-tenant `tenant-<ats>-<tenant>`) |
| The email and label of each sign-in | The credential index in the workspace database (never the password) |
| The cooldowns | `policy.yml` |

## How to check it worked

```
applysmith doctor --only credentials
applysmith credentials list
```

`credentials.vault` in the doctor reports index entries whose secret is gone
and environment variable names that two stored secrets share.

## If something goes wrong

| What you see | What to do |
|---|---|
| Account creation is refused with `cooldown_active` | A challenge, lockout or rate limit on that site started a cooldown; Settings, Applications shows when it ends. |
| A site shows a lockout | Applysmith stops that host for the day after three refused passwords, or at once on lockout wording. Reset the password yourself. |

Next: [Step 14: captcha solver](14-captcha.md).

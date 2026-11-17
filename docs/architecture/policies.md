# Policies

These are the safety rules Applysmith enforces. They are rules of the product,
kept in code and in the database, not conventions. Each is stated here at the
level a user needs; the normative text is in the specs
(`apply-safety-gates`, `apply-chain`, `remote-access`, `workspace-and-config`,
`credentials-vault`, `answer-bank`), and the implementation notes are in
[gates.md](gates.md), [captcha.md](captcha.md) and
[remote-access.md](remote-access.md). Every gate decision is written to an
audit table; the table is kept for 180 days.

The reasoning behind all of them is one sentence: an application is a
statement made in your name, so the product does only what you asked, says
only what is true of you, and stops when it is unsure.

## Submitting an application

- **Submit needs your explicit request.** A final Submit is pressed only when
  the request that started the run was your explicit request to apply and the
  run's `submit` input is true. Without it the run is a dry run: it fills every
  page and stops at Submit. In the command line the request is `--submit`; in
  the dashboard it is a confirmation that names the company, role and posting
  host; in the Ask Agent it is read from your own message in the current turn,
  never from a value the model chose. A question ("should I apply to ...?") and
  text you pasted (a job description's "Apply now") are not a request, and
  neither is "fill it in and press the last button": only apply or submit is.
- **Only two tools can submit**, the single-posting tool and the apply-list
  tool. Any other tool that claims the capability, and any tool whose name
  contains "submit" or "withdraw", is refused.
- **The agent holds when something outside steered it.** If the turn has read
  outside content (an email, a posting, a web page), or your message does not
  name the posting's site and the posting itself is not on the day's apply
  list, the submit is held until you confirm in a new message. The list vouches
  for the postings on it, never for every posting of a site one of them is
  on. A URL you wrote exactly always counts. A confirmation is a message from
  you, never the assistant's own text.
- **Unattended work never submits.** A schedule, a digest or background agent
  run, a Telegram turn, the read-only MCP server and a paired remote device
  cannot start a submitting run, fill a form, create an account, solve a
  captcha or confirm a held submit.
- **A posting is applied to once.** A submitting run is blocked by standing
  evidence of a previous application (a submission in flight or confirmed, or
  an applied tracker row for the same posting or the same company and role).
  An unconfirmed earlier submission can be lifted only by an explicit resubmit
  request. A confirmed one is re-sent only as a correction you ask the Ask
  Agent for by the company's name, once per posting, recorded as replacing
  the earlier submission and never touching the tracker. Unreadable evidence
  blocks too.
- **Withdraw and discard are never pressed.** The walk presses only recognised
  navigation labels (Continue, Next, Review, and similar). An unfamiliar,
  destructive or terminal label is refused, and so is a label that reads as
  Submit only through lookalike or hidden characters.
- **A page that disagrees with your profile blocks Submit.** After each page is
  filled, name, email, phone and link fields are compared with the profile; a
  mismatch, a value the run wrote that the page does not hold, or a required
  control left unanswered stops the run before Submit. An advisory model review
  can add flags but can never allow a Submit.
- **Employers on your skip list are left to you.** In a batch, a company on
  `batch_skip_companies` in `profile.yml` is skipped whatever site it uses, even
  if you named it. Absent the key, the list is Apple, Microsoft, Alphabet,
  Amazon, Meta, NVIDIA and Tesla; a key you set is the whole list, and an empty
  list skips nobody. An explicit single-posting request still applies. The
  agent removes a company from the list only when you name the list and that
  company; a removal in bulk waits for your confirmation, company by company.

## What is never invented

- **Self-identification and work authorization.** Race or ethnicity, gender,
  disability, veteran status, orientation, age, religion, citizenship and
  sponsorship or authorization questions are answered only with what you wrote
  in your profile or answer bank. If you wrote nothing, the product uses the
  page's own "decline to answer" option for self-identification, and leaves a
  work-authorization question for you. A request for proof of a fact is that
  fact (your decision, 2026-10-27, widened 2026-10-29): proof of work
  authorization is your authorization answer, proof of age your 18-or-older
  answer, and "can you verify your identity on hire?" is Yes once your profile
  holds your full name.
- **Employment history and experience (owner decision).** "Have you ever
  worked at / are you employed by <company or this company>?" is Yes when that
  company is in your employers list or work history, else No. An experience
  Yes/No ("Do you have 5+ years of X?") is Yes only when the model cites your
  CV, profile, bank or knowledge base showing it, else No; never a typed claim.
- **Statements about the application.** "Did you submit a cover letter?" is
  answered only from what the run observes (a filled cover-letter slot or
  letter box, or a letter it attached); "were you referred?", "did you use AI?" or
  "is everything in this application true and your own?" only from an entry you
  wrote yourself, never from a default or the legal-declaration setting, even
  when it also certifies that the content is true. Otherwise the field is left
  blank, and the Answer Bank page asks it until you answer.
- **A start phrase.** Owner decision (2026-10-19): when the posting asks the
  first written answer to begin with a quoted phrase, an answer the run writes
  there begins with it, and "did you start with the exact phrase?" is Yes only
  when the page holds it so; otherwise both are yours. An instruction to an AI
  or a bot is never followed.
- **Signatures.** A signature or initials box is filled only with your curated
  full-name answer or its initials. Nothing is composed, inferred or taken from
  outside content, and a signature that accompanies a disclosure the run did not
  itself answer from your profile is left blank.
- **Evidence in documents.** A generated résumé or cover letter keeps each
  sentence tied to evidence in your CV and knowledge base; an edit that loses
  the evidence is refused.

## Legal declarations and marketing opt-ins

- **A setting decides required declarations.** A required privacy or terms
  acknowledgement, a "the information is true" certification, a
  background-check authorization, an at-will acknowledgement and an e-signature
  consent are ticked only when the answer-bank runtime setting
  `autoAcceptLegalDeclarations` is on. It is on by default (owner decision
  2026-10-25) and lives in the
  workspace state (Settings, runtime settings), not in a file you might share. Off,
  each is left as a needs-you item and no later step ticks it instead. A
  create-account form with a required agreement is refused before any password
  is typed when the setting is off.
- **Optional marketing is never ticked.** Newsletters, job alerts, text
  messages, talent communities, "contact me about future roles" and sharing with
  other companies are never ticked, with the setting on or off. The same wording
  on a control the page marks required follows the setting. An answer you wrote
  yourself in your own answer bank applies as written.

## Applications the site already holds

- **An unfinished application is completed, not handed back.** Owner decision
  (2026-10-27): when the posting page shows that an application was started
  and not finished, the run continues it and fills every page; the final
  Submit still follows the run (a fill-only run never presses it). ADP
  registers an unfinished application the moment a signed-in candidate presses
  Apply and then shows "Applied" in its place; that control continues it.

## Accounts, passwords and links

- **Creating an account.** Only inside a run you started, whether it submits
  or only fills (F-8); a batch the app resumed after a restart creates none,
  and a form that is also the application creates its account only in a run
  that submits. Only on the supported systems, with no cooldown running for the
  system and no conflict with the declarations setting. There is no risk
  acknowledgement to sign and no weekly quota: the owner removed both
  (2026-10-25).
- **Cooldowns.** A risk signal (captcha, rate limit, account lock, required
  verification, login redirect, silent degradation) backs off that site for the
  hours set in `policy.yml`.
- **Sign-in sync from your everyday Chrome.** Owner authorisation
  (2026-09-30): at any sign-in page, before it tries a password, a run may ask
  the Applysmith extension in your everyday Chrome for the sign-in of exactly the
  site it is on, once per site per run (F-7). google.com and youtube.com are
  never copied, the extension is waited on for at most 45 seconds, and an undo
  snapshot is kept for 7 days on the Browser page.
- **Passwords cannot lock you out.** At most 3 refused passwords per site per
  run, a refused password is never tried again, and lockout wording stops the
  site for the day.
- **Sign-in links are never opened by a run.** A single-use sign-in link from
  mail is for you to open. The only mail links a run may open are an account
  activation link and a password-reset link, each under four locks: the link's
  host equals the tenant being used, the URL has that system's activation or
  reset shape with an opaque token, the sender is that system's own domain, and
  the link is the mail's own link (a reset: the link's target only; an
  activation: also an address the mail prints as text, as Workday's own tenant
  does; your decision 2026-10-29). A reset runs only for a known Workday
  account whose saved password was refused, at most once per host per run, and
  sets only the password you saved.
- **Codes.** Reading a verification code from mail for the current sign-in is
  allowed, bounded, and never repeated within a tab.

## The captcha solver

Off unless you have a 2Captcha key and `captcha_solver` in `policy.yml` is `on`
(the default). It acts only in an explicit, user-started run that submits, never
in a scan, dry run, learning replay or unattended run. It clears reCAPTCHA v2
(and Enterprise), hCaptcha, and a Turnstile box embedded in an application page;
it does not touch reCAPTCHA v3, full-page Cloudflare checks, image puzzles,
Arkose/FunCaptcha or DataDome. At most 2 solves per run; the account balance is
read once per run and under USD 0.50 leaves the challenge to you; never for an
employer on your skip list; no redirect is followed, so the key goes only to the
service. The key never appears in a log or a record.

## Keeping you in control of the browser

- Runs never raise or focus a window. The apply window is yours to see.
- Automation is detached from a tab kept for you within 2 seconds, and at most 3
  tabs are kept.
- Only `http:`, `https:`, `about:blank`, `blob:` and `data:` pages load in the
  managed browser; every permission prompt is denied except clipboard write and
  fullscreen. Scan windows never save downloads.
- The product drives only your installed Google Chrome, in its own profile. It
  never touches your everyday Chrome profile, and it refuses to use that
  directory.

## The agent at your desktop

The Ask Agent can work in other windows through PersonalCU, your own
computer-use service (owner decisions AC-1 to AC-5). PersonalCU holds the
Accessibility and Screen Recording permissions; Applysmith asks for neither.

- **Only when you ask, in the dashboard.** The desktop tools are offered only in
  an Ask Agent turn you typed in the dashboard or started with "Let the agent
  finish". A Telegram turn, a paired remote device, a schedule, a digest and the
  read-only MCP server never get them.
- **Only the windows you allowed.** The agent acts in the apply browser and in
  an app your message names. It never acts in Applysmith itself, PersonalCU, a
  terminal or another app that runs commands or agents (code editors, script
  and automation tools, AI apps), System Settings, Keychain Access, a password
  manager or your everyday Chrome.
- **Never the last button.** It never presses a control that reads as Submit,
  Withdraw, Discard, Delete, Pay, Buy or Send, never presses Return, types no
  text that carries a Return, Tab or other control character, and never
  activates or raises a window. A click must land on a control it has just read;
  a control it cannot read is not pressed, by a key either: Space, Backspace
  and Delete go only into a text field it has just clicked or typed into. In
  the apply browser it presses only what a run's walk may press and types
  nothing. An application is still submitted only by the apply chain's own
  Submit, under every rule above.
- **What it sees is outside content.** Looking at a window taints the turn, so a
  submit after it is held until you confirm. The model gets that window's
  screenshot when it takes images, and the window's text otherwise. The whole
  screen is never captured, and password and other secure fields are never read.
- **Answers still come from you.** When it finishes a stopped application, each
  answer must be one from your answer bank or profile, or words you wrote in the
  conversation; it asks you for anything else. The run then stops at Submit for
  your confirmation.
- **You can stop it.** Pause on the Agent control page refuses every desktop
  action at once and stays on across restarts; Stop ends the turn. Each desktop
  decision is audited (tool, app, the control's role, the verdict), never the
  text typed.

## Remote access

Off by default and never on a local network: the only remote path is an HTTPS
tunnel you start with `applysmith remote on tunnel` (explicit confirmation), and
every listener the server opens binds a loopback address.

- A request is remote because of the listener it arrived on, never a header.
- A paired device (QR pairing, a code valid for 300 seconds, once) gets only the
  routes on a pinned allowlist. It never reaches configuration, the answer bank,
  secrets, credentials, pairing administration, server shutdown or the browser
  session routes.
- A remote chat gets the read and draft tools only. A device cannot start a
  submitting run, confirm a held submit or approve a proposed rule.
- The apply and scan windows show status to a device but cannot be controlled
  by one, and screenshots are served only if `allow_screenshots` is set to true
  in `policy.yml` (default false), as stills, one per 2 seconds.
- 20 bad credentials from one source in a minute block it for 10 minutes. You
  can list devices and revoke any or all of them at any time.
- Only you can edit `policy.yml`: through the API only the dashboard session on
  the loopback listener may write it; the command line edits the file directly.

## Data and privacy

- Your data stays on your computer, in a folder you own; secrets stay in the
  keychain ([storage.md](storage.md)).
- Gmail access is read-only metadata; a mail link is never followed by the
  product on your behalf, and mail text is shown as text.
- A tab handed to you is watched only to learn the answers you commit, for at
  most 2 hours, never recording passwords, one-time codes, card or national
  identifier fields, your own name, email, phone or date of birth, or
  self-identification answers.
- The terms of use of the sites you scan and apply to remain yours to read and
  follow.

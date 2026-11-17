// The two films as data: scenes in order, each with its length in seconds.
// Every claim comes from README.md, docs/setup and docs/user-guide; the
// screenshots under shots/ are of the demo workspace only.
const FILMS = {
  intro: {
    scenes: [
      { kind: "title", dur: 5, icon: true, title: "Applysmith", text: "A local-first job-search tool that runs on your own computer." },
      {
        kind: "cards", dur: 8, cols: 4, kicker: "What it does", title: "From a posting to a filled application",
        text: "One tool for the whole search, around one server on your Mac.",
        cards: [
          { n: "1", h: "Find", p: "Scans job sources, hiring signals and leads into one inbox." },
          { n: "2", h: "Evaluate", p: "Scores each posting against you, with the evidence behind the score." },
          { n: "3", h: "Prepare", p: "Tailors a résumé and a cover letter, grounded in what you really did." },
          { n: "4", h: "Apply", p: "Fills application forms in a Chrome window of its own." },
        ],
      },
      {
        kind: "cards", dur: 8, cols: 3, kicker: "Local-first", title: "Your data stays on your machine",
        text: "Nothing leaves the computer except calls to the services you connect.",
        cards: [
          { h: "One server, loopback only", p: "A single Go process per workspace serves the API, the dashboard, the scheduler and the agent." },
          { h: "A folder you choose", p: "SQLite for runtime state, plain files for everything you edit: profile, CV, answer bank, policy." },
          { h: "Secrets in the keychain", p: "Passwords and API keys go to the macOS keychain, never into the workspace." },
        ],
      },
      {
        kind: "cards", dur: 9, cols: 3, stagger: 0.28, kicker: "The pieces", title: "One server, many ways in",
        cards: [
          { h: "Desktop app", p: "A window, a menu-bar item and notifications." },
          { h: "Dashboard", p: "Eighteen pages in five groups. Command-K jumps to any of them." },
          { h: "Command line", p: "applysmith: setup, doctor, scans, evaluation, tracker, apply, backup." },
          { h: "Chrome extension", p: "A side panel and page widgets for your everyday Chrome." },
          { h: "Browser lanes", p: "A dedicated Chrome: a visible apply window and scan windows." },
          { h: "Ask Agent", p: "A chat assistant with a closed set of tools and a guard chain." },
        ],
      },
      { kind: "shot", dur: 6.5, img: "home", path: "/", kicker: "Dashboard · Home", title: "Today at a glance", text: "What is ready, what needs you, and how the system is doing.", scroll: [150, 330] },
      { kind: "shot", dur: 7, img: "inbox", path: "/discovery?tab=inbox", kicker: "Moves 1 and 2 · Scan and Inbox", title: "Find postings worth a look", text: "Scans can run on a schedule.", scroll: [130, 200] },
      { kind: "shot", dur: 7.5, img: "report", path: "/reports?id=1", kicker: "Move 3 · Evaluate", title: "A score, a band and the evidence", text: "Shown: the demo, where an offline stand-in simulates the score.", scroll: [330, 560] },
      { kind: "shot", dur: 7, img: "answer-bank", path: "/answer-bank", kicker: "Prepare · Answer Bank", title: "The answers forms are filled from", text: "Work-authorization answers come from you.", scroll: [130, 400] },
      { kind: "shot", dur: 7.5, img: "runs", path: "/runs", kicker: "Moves 4 and 5 · Apply and the Runs board", title: "Fill, review, submit", text: "A dry run fills every page and stops before Submit.", scroll: [130, 210] },
      { kind: "shot", dur: 7, img: "tracker", path: "/tracker", kicker: "Moves 6 and 7 · Tracker and Gmail", title: "Every application and its status", text: "Connect Gmail and replies move the statuses for you.", scroll: [150, 380] },
      { kind: "shot", dur: 7, img: "ask", path: "/ask", kicker: "Move 8 · Ask Agent and Telegram", title: "Ask, draft, get nudged", text: "A call that needs your approval waits for you.", scroll: [130, 210] },
      {
        kind: "cards", dur: 10, cols: 2, kicker: "Policies", title: "Rules that always hold",
        text: "The product enforces them, so you do not have to remember them.",
        cards: [
          { h: "Submit is yours", p: "Submitting needs your explicit request for that application, or a confirmation you complete in the dashboard." },
          { h: "No invented answers", p: "A work-authorization or self-identification question without your answer is left blank." },
          { h: "No duplicates", p: "A posting you apply to twice is stopped." },
          { h: "Schedules never submit", p: "A schedule can scan, read mail and send digests. It can never fill a form, create an account or submit." },
        ],
      },
      { kind: "title", dur: 6.5, icon: true, small: true, title: "Your job search, on your own computer.", text: "Try the demo first: a fictional persona, three sample postings, nothing of yours.", cmd: "applysmith demo init" },
    ],
  },

  setup: {
    scenes: [
      { kind: "title", dur: 5, icon: true, kicker: "Setup and first use", small: true, title: "Set up Applysmith", text: "From nothing to a first evaluated posting." },
      {
        kind: "cards", dur: 6.5, cols: 3, kicker: "Before you start", title: "What this computer needs",
        text: "Step 0 checks all three, and installs nothing.",
        cards: [
          { h: "macOS 13 or later", p: "Other operating systems are unverified." },
          { h: "Google Chrome 120 or later", p: "The apply window is a Chrome window of its own." },
          { h: "2 GiB of free disk", p: "For the database and the browser profile." },
        ],
      },
      {
        kind: "cards", dur: 11, cols: 3, kicker: "Install", title: "Three moves, once",
        text: "Building from source instead? Run make build and use bin/applysmith.",
        cards: [
          { n: "1", h: "Drag to Applications", p: "Open the disk image and drag Applysmith to the Applications folder." },
          { n: "2", h: "Allow the first launch", p: "The app is not notarized. In System Settings, Privacy & Security, choose Open Anyway." },
          { n: "3", h: "Install the command", p: "In Applysmith: Settings, Advanced, Command line, Install the command." },
        ],
      },
      {
        kind: "terminal", dur: 10, kicker: "See it working first", title: "Start with the demo",
        text: "A separate folder with the fictional persona Alex Example. It never presses Submit.",
        steps: [
          { cmd: "applysmith demo init", out: ["demo workspace ~/Applysmith-Demo: the fictional persona Alex Example."] },
          { cmd: "applysmith serve --workspace ~/Applysmith-Demo", out: ["Applysmith server for ~/Applysmith-Demo listening on http://127.0.0.1:47319", "Open the dashboard: http://127.0.0.1:47319/?launch=..."] },
        ],
      },
      { kind: "shot", dur: 6.5, img: "home", path: "/", kicker: "The demo workspace", title: "Look around as Alex Example", text: "Your own workspace is untouched.", scroll: [0, 200] },
      {
        kind: "terminal", dur: 11, kicker: "Steps 0 and 1", title: "Check the computer, create the workspace",
        text: "The workspace is one folder that holds everything that is yours: ~/Applysmith by default.",
        steps: [
          { cmd: "applysmith setup apply prerequisites", out: ["prerequisites: complete"] },
          { cmd: "applysmith init", out: ["created profile.yml", "created portals.yml", "created policy.yml", "created cv.md", "created narrative.md", "created answer-bank.yml"] },
        ],
      },
      {
        kind: "steps", dur: 12, kicker: "The steps", title: "Eighteen steps, twelve required",
        text: "Follow them in docs/setup, with applysmith setup, or on the Setup page. All three read the same list.",
        steps: [
          "Check this computer", "Create the workspace", "Your CV", "Identity and contact details", "Work authorization and location", "What you are looking for",
          "Your story and voice", "Résumé PDFs", "Answer bank review", "Language-model provider", "Apply window", "Gmail?",
          "Telegram?", "Sign-in credentials?", "Captcha solver?", "Job sources and schedule?", "Chrome extension?", "Orientation",
        ],
      },
      { kind: "shot", dur: 7.5, img: "setup", path: "/setup", kicker: "The Setup page", title: "One step at a time", text: "Each step shows what is left and its commands.", scroll: [400, 540] },
      { kind: "shot", dur: 7.5, img: "profile", path: "/profile", kicker: "Steps 2 to 7 · About you", title: "CV, identity, targets, story, résumés", text: "Import your CV from a file, then review it.", scroll: [130, 420] },
      { kind: "shot", dur: 7, img: "answer-bank", path: "/answer-bank", kicker: "Step 8 · Answer bank review", title: "The answers forms are filled from", text: "Entries marked To answer wait for you.", scroll: [130, 420] },
      {
        kind: "cards", dur: 10, cols: 4, kicker: "Step 9 · Language-model provider", title: "Connect one provider, your own account",
        text: "A key is a secret: it goes to the keychain through a hidden prompt, never a command argument.",
        cards: [
          { h: "A preset", p: "An API key from OpenRouter, DeepSeek, OpenAI, Anthropic, Gemini and others." },
          { h: "A signed-in tool", p: "codex or claude on the command line. Plain completion only." },
          { h: "Your ChatGPT plan", p: "Sign in with ChatGPT. No key; the plan's own limits apply." },
          { h: "Offline", p: "A stand-in provider with no key. For looking around, not for applying." },
        ],
      },
      { kind: "shot", dur: 7, img: "browser", path: "/browser", kicker: "Step 10 · Apply window", title: "A Chrome window of its own", text: "Your everyday Chrome is never touched.", scroll: [130, 300] },
      {
        kind: "cards", dur: 9.5, cols: 3, stagger: 0.28, kicker: "Steps 11 to 16", title: "Optional connections",
        cards: [
          { h: "Gmail", p: "Replies move statuses; job email becomes briefs and actions." },
          { h: "Telegram", p: "Notifications and nudges from your own bot." },
          { h: "Sign-in credentials", p: "A shared default sign-in for application sites." },
          { h: "Captcha solver", p: "A solver key of your own, kept in the keychain." },
          { h: "Job sources and schedule", p: "What to scan and how often: none, daily or twice daily." },
          { h: "Chrome extension", p: "A side panel in your everyday Chrome, paired to the server." },
        ],
      },
      {
        kind: "terminal", dur: 12, kicker: "Where am I?", title: "Three commands tell you",
        text: "status lists every step, next shows the first one left, doctor runs every check.",
        steps: [
          { cmd: "applysmith setup status", out: ["progress 75%", " 8 answer-bank          complete  required", " 9 llm                  missing   required", "     - no provider is configured: choose one, or offline until you have a key"] },
          { cmd: "applysmith setup next" },
          { cmd: "applysmith doctor", out: ["[PASS]    prerequisites.platform      macOS 15.0", "[PASS]    prerequisites.chrome        Google Chrome 150.0.0.0", "[PASS]    prerequisites.disk          40.0 GiB free"] },
        ],
      },
      { kind: "shot", dur: 7, img: "inbox", path: "/discovery?tab=inbox", kicker: "First use · Discovery, Inbox", title: "Evaluate your first posting", text: "Press Evaluate on a row.", scroll: [130, 210] },
      { kind: "shot", dur: 7, img: "report", path: "/reports?id=1", kicker: "First use · Reports", title: "Read the report", text: "The score, the band and the evidence behind each part.", scroll: [330, 560] },
      { kind: "shot", dur: 7.5, img: "runs", path: "/runs", kicker: "First use · Runs", title: "Then a dry run", text: "A dry run fills the form and stops before Submit.", scroll: [130, 210] },
      {
        kind: "cards", dur: 8, cols: 3, kicker: "Every day", title: "After setup",
        text: "A day is a loop of eight moves; the user guide walks through each one.",
        cards: [
          { h: "Open the app", p: "Or run applysmith serve. Either one is the server for your workspace." },
          { h: "Jump with Command-K", p: "Eighteen pages; filters and open rows live in the address." },
          { h: "Backed up at 02:00", p: "A verified snapshot beside the workspace every night, the newest 14 kept." },
        ],
      },
      { kind: "title", dur: 6.5, icon: true, small: true, title: "You are set.", text: "The full guide is in docs/setup, one page per step.", cmd: "applysmith setup next" },
    ],
  },
};

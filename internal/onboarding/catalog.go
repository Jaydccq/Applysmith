package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"applysmith/internal/gmail"
)

// StepStatus is the verdict of a completion detector.
type StepStatus string

const (
	StepMissing  StepStatus = "missing"
	StepTemplate StepStatus = "template"
	StepPartial  StepStatus = "partial"
	StepComplete StepStatus = "complete"
	StepSkipped  StepStatus = "skipped"
)

// Phases of the catalog.
const (
	PhaseFoundations = "foundations"
	PhaseAboutYou    = "about-you"
	PhaseConnections = "connections"
	PhaseTools       = "tools"
	PhaseStart       = "start"
)

// RequiredSteps is the number of required catalog steps; progress divides by it.
const RequiredSteps = 12

// Field declares one input of a step writer. The schema output of every lane,
// the unknown-field check and the per-field errors all read this list.
type Field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // string, bool, int, string-list, object, object-list
	Required bool     `json:"required,omitempty"`
	Doc      string   `json:"doc"`
	Enum     []string `json:"enum,omitempty"`
}

// Detector decides a step's status from the stored data. It never writes.
type Detector func(ctx context.Context, env *Env) Detection

// Writer validates a step's input, writes atomically and returns the new
// status. With dry set it validates and writes nothing.
type Writer func(ctx context.Context, env *Env, input json.RawMessage, dry bool) (Applied, error)

// Step is one entry of the catalog. Every lane reads its list, order, titles,
// flags and progress from here and carries no step list of its own.
type Step struct {
	N        int
	ID       string
	Title    string
	Phase    string
	Required bool
	// Purpose says what stops working while the step is undone.
	Purpose string
	// SkipCost is what an optional step loses when skipped.
	SkipCost string
	// Storage says where the step's data lives.
	Storage  string
	Schema   []Field
	Commands []string
	Guide    string
	Detect   Detector
	Apply    Writer
	// AckScope returns the content an acknowledgement is bound to; nil when
	// the step has nothing to acknowledge.
	AckScope func(ctx context.Context, env *Env) (string, error)
	// Human is set for a step that needs a person; it holds the instruction
	// as a terminal reads it.
	Human string
	// DashboardHuman, when set, is that instruction as the dashboard reads it
	// (Env.InServer), the app's window included: it names the dashboard's own
	// controls where Human names commands, so nobody in the app is sent to a
	// terminal.
	DashboardHuman string
	// Page is the dashboard page that hosts the feature when the step has no
	// form of its own.
	Page string
	// Inputless marks a writer that takes no input, so it declares no schema.
	Inputless bool
}

var catalog []Step

// The catalog is built in init because its detectors read the catalog.
func init() {
	catalog = []Step{
		{N: 0, ID: "prerequisites", Title: "Check this computer", Phase: PhaseFoundations, Required: true,
			Purpose: "Without macOS 13 or later, Google Chrome 120 or later and 2 GiB of free disk the apply window and the database cannot run.",
			Storage: "nothing is stored; the step only probes the machine", Inputless: true, Commands: []string{"applysmith setup apply prerequisites", "applysmith doctor --only prerequisites"},
			Detect: detectPrerequisites, Apply: applyPrerequisites},
		{N: 1, ID: "workspace", Title: "Create the workspace", Phase: PhaseFoundations, Required: true,
			Purpose: "Without a workspace there is nowhere to keep your profile, CV and applications.",
			Storage: "the workspace directory and the state directory with its SQLite database", Schema: workspaceFields,
			Commands: []string{"applysmith setup apply workspace --file <path|->", "applysmith init"},
			Detect:   detectWorkspace, Apply: applyWorkspace},
		{N: 2, ID: "cv", Title: "Your CV", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without a CV nothing can be evaluated against your experience and no résumé can be written.",
			Storage: "cv.md, and the confirmed history and skills keys of the profile file", Schema: cvFields,
			Commands: []string{"applysmith setup apply cv --file <path|->", "applysmith setup ack cv"},
			Detect:   detectCV, Apply: applyCV, AckScope: scopeCV},
		{N: 3, ID: "identity", Title: "Your identity and contact details", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without them application forms cannot be filled with your name, email and address.",
			Storage: "the profile file", Schema: identityFields,
			Commands: []string{"applysmith setup apply identity --file <path|->", "applysmith setup ack identity"},
			Detect:   detectIdentity, Apply: applyIdentity, AckScope: scopeIdentity},
		{N: 4, ID: "work-authorization", Title: "Work authorization and location", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without them sponsorship and authorization questions are left blank and postings you cannot take are not filtered.",
			Storage: "the profile file and the personal entries of the answer bank", Schema: workAuthFields,
			Commands: []string{"applysmith setup apply work-authorization --file <path|->"},
			Detect:   detectWorkAuth, Apply: applyWorkAuth},
		{N: 5, ID: "targets", Title: "What you are looking for", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without targets nothing can be ranked and the eligibility gate has nothing to compare against.",
			Storage: "the profile file", Schema: targetsFields,
			Commands: []string{"applysmith setup apply targets --file <path|->", "applysmith setup ack targets"},
			Detect:   detectTargets, Apply: applyTargets, AckScope: scopeTargets},
		{N: 6, ID: "story", Title: "Your story and voice", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without a narrative, cover letters and free-text answers have nothing of yours to draw on.",
			Storage: "narrative.md and voice.md", Schema: storyFields,
			Commands: []string{"applysmith setup apply story --file <path|->", "applysmith setup skip story.voice"},
			Detect:   detectStory, Apply: applyStory},
		{N: 7, ID: "resumes", Title: "Résumé PDFs", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without a résumé PDF there is nothing to upload to an application.",
			Storage: "resumes/ and the résumé roster in the profile file", Schema: resumeFields,
			// New PDFs need the document toolchain; these commands diagnose and prepare it.
			Commands: []string{"applysmith setup apply resumes --file <path|->", "applysmith doctor --only resumes", "applysmith documents toolchain setup --yes", "applysmith documents toolchain smoke"},
			Detect:   detectResumes, Apply: applyResumes},
		{N: 8, ID: "answer-bank", Title: "Answer bank review", Phase: PhaseAboutYou, Required: true,
			Purpose: "Without a reviewed answer bank, form questions are left for you or answered from unreviewed defaults.",
			Storage: "answer-bank.yml", Schema: answerBankFields,
			Commands: []string{"applysmith setup apply answer-bank --file <path|->", "applysmith answer-bank status"},
			Detect:   detectAnswerBank, Apply: applyAnswerBank},
		{N: 9, ID: "llm", Title: "Language-model provider", Phase: PhaseConnections, Required: true,
			Purpose: "Without a working provider nothing is evaluated, written or answered by a model.",
			Storage: "keychain for keys; provider choice and routing in SQLite", Schema: llmFields,
			Commands: []string{"applysmith llm configure --preset <name> --key-stdin", "applysmith llm configure --cli codex|claude", "applysmith llm signin chatgpt", "applysmith llm test", "applysmith setup apply llm --file <path|->"},
			Detect:   detectLLM, Apply: applyLLM, Human: "Create an API key at the preset's key page and pipe it to `applysmith llm keys set <provider>`; an agent must not ask for it in chat.",
			DashboardHuman: "Create an API key at the preset's key page, paste it into API key on this step and press Save and test; an agent must not ask for it in chat."},
		{N: 10, ID: "browser", Title: "Apply window", Phase: PhaseConnections, Required: true,
			Purpose: "Without the apply window nothing can be applied to or scanned in a browser.",
			Storage: "the Chrome profile in the state directory", Inputless: true, Page: "/browser",
			Commands: []string{"applysmith setup apply browser", "applysmith browser check", "applysmith doctor --only browser"},
			Detect:   detectBrowser, Apply: applyBrowser,
			Human:          "Run `applysmith setup apply browser`: it opens the apply window if it is not open and checks it. While the app or `applysmith serve` holds this workspace, press Check the apply window in its setup instead. The window uses its own Chrome profile; your everyday Chrome is not touched.",
			DashboardHuman: "Press Check the apply window: it opens the apply window if it is not open and checks that it does not tell websites it is automated. The window uses its own Chrome profile; your everyday Chrome is not touched."},
		{N: 11, ID: "gmail", Title: "Gmail", Phase: PhaseConnections,
			Purpose: "Reads mail read-only over IMAP for outcomes and codes.", SkipCost: "OTP codes, outcome tracking and digests are unavailable without Gmail.",
			Storage: "keychain for the app password; the address and non-secret metadata in SQLite", Inputless: true,
			Commands: []string{gmail.ConnectCommand, "applysmith gmail test", "applysmith gmail status"},
			Detect:   detectGmail, Apply: humanOnly(gmailHuman), Page: "/settings#settings-gmail", Human: gmailHuman,
			DashboardHuman: "Turn on 2-Step Verification for your Google Account, then create an app password (Google Account, Security, App passwords). Enter your Gmail address and the app password in Settings, Gmail and press Connect: Gmail is signed in to before anything is stored. Some Google Workspace administrators turn app passwords off."},
		{N: 12, ID: "telegram", Title: "Telegram", Phase: PhaseConnections,
			Purpose: "Sends notifications and answers chat commands.", SkipCost: "No Telegram notifications or chat.",
			Storage: "keychain for the bot token; the paired chat in SQLite", Inputless: true,
			Commands: []string{"applysmith telegram set-token --stdin", "applysmith telegram pair"},
			Detect:   detectTelegram, Apply: humanOnly(telegramHuman), Page: "/settings#settings-telegram", Human: telegramHuman,
			DashboardHuman: "Open the BotFather chat in Telegram, send /newbot, choose a display name and a username ending in bot, and copy the token. In Settings, Telegram, paste it into Bot token and press Save token, then press Get a pairing code and send the code to your bot from a private chat."},
		{N: 13, ID: "credentials", Title: "Sign-in credentials", Phase: PhaseConnections,
			Purpose: "Signs in to application sites on your behalf.", SkipCost: "Sign-in and account creation on application sites are left to you.",
			Storage: "keychain for passwords; label index in SQLite", Inputless: true,
			Commands: []string{"applysmith credentials set-default --email <address> --password-stdin"},
			Detect:   detectCredentials, Apply: humanOnly(credentialsHuman), Page: "/settings#settings-signin", Human: credentialsHuman},
		{N: 14, ID: "captcha", Title: "Captcha solver", Phase: PhaseConnections,
			Purpose: "Clears supported challenges inside an explicit apply run.", SkipCost: "Challenges are left to you.",
			Storage: "keychain", Inputless: true, Commands: []string{"applysmith doctor --only captcha"},
			Detect: detectCaptcha, Apply: humanOnly(captchaHuman), Page: "/settings#settings-captcha", Human: captchaHuman,
			DashboardHuman: "Create an account at 2captcha.com and copy its API key. In Settings, Captcha solver, paste it into 2Captcha API key and press Save key; it is stored in the keychain. " + captchaBounds + " The Use the solver switch in the same section turns it off while the key stays stored; it is on by default."},
		{N: 15, ID: "discovery", Title: "Job sources and schedule", Phase: PhaseTools,
			Purpose: "Finds postings on a schedule.", SkipCost: "No postings are found automatically; add them by hand.",
			Storage: "portals.yml; registry and schedules in SQLite", Schema: discoveryFields,
			Commands: []string{"applysmith setup apply discovery --file <path|->", "applysmith schedule packs", "applysmith schedule apply-pack <pack>"},
			Detect:   detectDiscovery, Apply: applyDiscovery},
		{N: 16, ID: "extension", Title: "Chrome extension", Phase: PhaseTools,
			Purpose: "Adds the side panel and page widgets in your everyday Chrome.", SkipCost: "The panel, page widgets and session sync are unavailable.",
			Storage: "the paired-device record in SQLite", Inputless: true,
			Commands: []string{"applysmith extension export <dir>", "applysmith doctor --only extension"},
			Detect:   detectExtension, Apply: humanOnly(extensionHuman), Page: "/settings#settings-extension", Human: extensionHuman,
			DashboardHuman: "Open Settings, Browser extension and follow its steps to load the extension in Chrome; in the app it names the folder the app keeps. Then press Pair the extension there and type the code into the extension's popup within 120 seconds."},
		{N: 17, ID: "orientation", Title: "Orientation", Phase: PhaseStart, Required: true,
			Purpose: "Without it the daily loop and the skipped steps are never shown to you.",
			Storage: "the acknowledgement in SQLite", Inputless: true, Commands: []string{"applysmith setup ack orientation"},
			Detect: detectOrientation, Apply: applyOrientation, AckScope: scopeOrientation},
	}
	for i := range catalog {
		catalog[i].Guide = fmt.Sprintf("docs/setup/%02d-%s.md", catalog[i].N, catalog[i].ID)
	}
}

// Catalog returns the ordered 18-step catalog.
func Catalog() []Step { return append([]Step(nil), catalog...) }

// StepByID looks a step up.
func StepByID(id string) (Step, bool) {
	for _, s := range catalog {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

const (
	gmailHuman       = "Turn on 2-Step Verification for your Google Account, then create an app password (Google Account, Security, App passwords). Run `" + gmail.ConnectCommand + "` and paste the app password when asked; it is read from standard input, never from an argument, and Gmail is signed in to before anything is stored. An agent must not ask for it in chat. Some Google Workspace administrators turn app passwords off."
	telegramHuman    = "Open the BotFather chat in Telegram, send /newbot, choose a display name and a username ending in bot, and copy the token. Store it with `applysmith telegram set-token --stdin`, then run `applysmith telegram pair` and send the code it prints to your bot from a private chat."
	captchaHuman     = "Create an account at 2captcha.com and copy its API key. Store it in the keychain with `applysmith secrets set twocaptcha-key` (the key is read from standard input, never from an argument), or supply it through the APPLYSMITH_SECRET_TWOCAPTCHA_KEY environment variable of the server. " + captchaBounds + " The `captcha_solver` key in policy.yml turns it off while the key stays stored; it is on by default."
	credentialsHuman = "Store a shared default sign-in (an email and a password; the password goes to the keychain only) in Settings, Sign-in credentials, or with `applysmith credentials set-default --email <address> --password-stdin`, which reads the password from standard input or a hidden prompt. A run creates an account with it when an application needs one. Instead of it, or as well, turn on Sign in with Google in the same section: sign in to Google once in the apply window (Browser page), and a run then presses a site's Sign in with Google and uses that session. Codes and activation or reset mails are read from Gmail (step 11): a run that could not read one links Settings, Gmail."
	captchaBounds    = "The solver runs only inside an explicit apply run that submits, solves at most 2 challenges per run, stops under USD 0.50 of balance and never runs for an employer on the batch skip list."
	extensionHuman   = "Export the extension with `applysmith extension export <dir>`, open chrome://extensions, switch on Developer mode, choose Load unpacked and select the folder. Then enter the pairing code shown in Settings (valid for 120 seconds) in the extension's popup."
)

// Progress is the number of satisfied required steps over RequiredSteps,
// rounded to a whole percent. Optional steps never count.
func Progress(statuses map[string]Detection) int {
	n := 0
	for _, s := range catalog {
		if s.Required && statuses[s.ID].Status == StepComplete {
			n++
		}
	}
	return int(math.Round(float64(n) * 100 / RequiredSteps))
}

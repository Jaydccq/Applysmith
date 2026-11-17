package wire

// ApplicantSnapshot is the identity and history the autofill engine receives
// when a fill starts. A field with no value is absent; a field the user left to
// themselves is listed in AskMe and never valued.
type ApplicantSnapshot struct {
	Candidate ApplicantCandidate `json:"candidate"`
	Links     ApplicantLinks     `json:"links"`
	History   ApplicantHistory   `json:"history"`
	AskMe     []string           `json:"askMe"`
}

// ApplicantCandidate holds the person's names, contact details and address.
// The names and the phone parts are derived on the server so the engine never
// splits free text.
type ApplicantCandidate struct {
	FullName         string           `json:"fullName,omitempty"`
	FirstName        string           `json:"firstName,omitempty"`
	MiddleName       string           `json:"middleName,omitempty"`
	LastName         string           `json:"lastName,omitempty"`
	PreferredName    string           `json:"preferredName,omitempty"`
	Email            string           `json:"email,omitempty"`
	Phone            string           `json:"phone,omitempty"`
	PhoneCountryCode string           `json:"phoneCountryCode,omitempty"`
	PhoneNational    string           `json:"phoneNational,omitempty"`
	Address          ApplicantAddress `json:"address"`
}

// ApplicantAddress is the postal address; State is the profile's region.
type ApplicantAddress struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	County     string `json:"county,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

// ApplicantLinks are the profile links, written with a scheme.
type ApplicantLinks struct {
	LinkedIn  string `json:"linkedin,omitempty"`
	GitHub    string `json:"github,omitempty"`
	Portfolio string `json:"portfolio,omitempty"`
}

// ApplicantHistory holds the employment and education entries in profile order.
type ApplicantHistory struct {
	Employment []ApplicantJob   `json:"employment"`
	Education  []ApplicantStudy `json:"education"`
}

// ApplicantJob is one employment entry. Dates are ISO year and month
// (YYYY-MM) as the profile states them, never with an invented day; a current
// job has no end date.
type ApplicantJob struct {
	Employer  string `json:"employer"`
	Title     string `json:"title,omitempty"`
	Location  string `json:"location,omitempty"`
	Summary   string `json:"summary,omitempty"`
	StartDate string `json:"startDate,omitempty"`
	EndDate   string `json:"endDate,omitempty"`
	Current   bool   `json:"current"`
}

// ApplicantStudy is one education entry, dated like ApplicantJob.
type ApplicantStudy struct {
	School       string `json:"school"`
	Degree       string `json:"degree,omitempty"`
	Field        string `json:"field,omitempty"`
	GradeAverage string `json:"gradeAverage,omitempty"`
	StartDate    string `json:"startDate,omitempty"`
	EndDate      string `json:"endDate,omitempty"`
	InProgress   bool   `json:"inProgress"`
}

// ExtensionBuild names the extension embedded in this application.
type ExtensionBuild struct {
	Stamp   string `json:"stamp"`
	Version string `json:"version"`
}

// ExtensionClient is one extension install the local token served, named by
// the X-Applysmith-Extension header it sends. It never carries the token.
type ExtensionClient struct {
	ID       string `json:"id"`
	Version  string `json:"version,omitempty"`
	Build    string `json:"build,omitempty"`
	Managed  bool   `json:"managed,omitempty"`
	LastSeen Time   `json:"lastSeen"`
}

// ExtensionClients lists the installs seen since the server started (Since),
// most recently seen first. The list is kept in memory only.
type ExtensionClients struct {
	Since   Time              `json:"since"`
	Clients []ExtensionClient `json:"clients"`
}

// PostingRead is a posting as its public board states it, for a page that
// holds only the application form (a Lever /apply or Ashby /application page).
type PostingRead struct {
	Title    string `json:"title"`
	Location string `json:"location"`
	Text     string `json:"text"`
}

// Run kind and the outcomes an extension fill ends with.
const (
	RunKindExtensionFill = "extension-fill"
	FillSucceeded        = "succeeded"
	FillCancelled        = "cancelled"
)

// FillRunRequest asks for a run to attach an extension fill to. URL has its
// query and fragment removed before it is kept.
type FillRunRequest struct {
	Host string `json:"host"`
	URL  string `json:"url"`
}

// FillRunEnd ends an extension fill: Status is succeeded or cancelled.
type FillRunEnd struct {
	Status string `json:"status"`
}

// SessionSiteCount is how many cookies the everyday Chrome holds for a site.
type SessionSiteCount struct {
	Domain  string  `json:"domain"`
	Cookies SafeInt `json:"cookies"`
}

// SessionCheckin carries per-site cookie counts; it has no names or values. The
// first check-in after the extension starts may carry none.
type SessionCheckin struct {
	Sites []SessionSiteCount `json:"sites"`
}

// SessionPending is the sign-in sync request the extension should answer.
type SessionPending struct {
	RequestID string   `json:"requestId"`
	Domains   []string `json:"domains"`
	ExpiresAt Time     `json:"expiresAt"`
}

// SessionCheckinReply lists the sites to count and the pending request, if any.
type SessionCheckinReply struct {
	Sites   []string        `json:"sites"`
	Pending *SessionPending `json:"pending" tstype:"SessionPending | null"`
}

// SessionCookie is one unpartitioned cookie from the everyday Chrome. Value is
// a secret: it is never logged, stored by the API or returned.
type SessionCookie struct {
	Name           string   `json:"name"`
	Value          string   `json:"value"`
	Domain         string   `json:"domain"`
	Path           string   `json:"path"`
	Secure         bool     `json:"secure"`
	HTTPOnly       bool     `json:"httpOnly"`
	HostOnly       bool     `json:"hostOnly"`
	Session        bool     `json:"session"`
	SameSite       string   `json:"sameSite"`
	ExpirationDate *float64 `json:"expirationDate,omitempty"`
}

// SessionImport answers a pending request. Skipped names requested domains the
// extension refused to read; Truncated says a cap cut the cookie list.
type SessionImport struct {
	RequestID string          `json:"requestId"`
	Cookies   []SessionCookie `json:"cookies"`
	Skipped   []string        `json:"skipped"`
	Truncated bool            `json:"truncated"`
}

// SessionPush copies one site's sign-in on the user's own click.
type SessionPush struct {
	Site      string          `json:"site"`
	Cookies   []SessionCookie `json:"cookies"`
	Truncated bool            `json:"truncated"`
}

// SessionImportResult counts what an import did, never a name or a value.
type SessionImportResult struct {
	Imported    SafeInt `json:"imported"`
	Failed      SafeInt `json:"failed"`
	Replaced    SafeInt `json:"replaced"`
	NotInSource SafeInt `json:"notInSource"`
	Truncated   bool    `json:"truncated"`
}

// BrowserOpenRequest asks for a page in the apply window, in a tab the user
// owns. The address is http or https without credentials.
type BrowserOpenRequest struct {
	URL string `json:"url"`
}

// BrowserOpened says the page was opened.
type BrowserOpened struct {
	Opened bool `json:"opened"`
}

// SignInCheck is the sign-in probe's answer for one job site. State is
// signed_in, signed_out, checkpoint, captcha, rate_limited or unknown; Next is
// proceed, retry-later, needs-user or blocked-by-safety. CheckedAt is null for
// a site the probe could not read and never read before, and Error then says
// why. Cached is true when the last result was served without loading a page.
type SignInCheck struct {
	Domain    string `json:"domain"`
	Label     string `json:"label"`
	State     string `json:"state"`
	Next      string `json:"next"`
	CheckedAt *Time  `json:"checkedAt" tstype:"Time | null"`
	Cached    bool   `json:"cached"`
	Error     string `json:"error"`
}

// SignInChecks is the result of one probe over every listed site.
type SignInChecks struct {
	Checks []SignInCheck `json:"checks"`
}

// SessionSyncRequest asks the everyday Chrome for the sign-in of sites.
type SessionSyncRequest struct {
	Sites []string `json:"sites"`
}

// SessionUndoResult counts what undoing the last import did.
type SessionUndoResult struct {
	Restored SafeInt `json:"restored"`
	Failed   SafeInt `json:"failed"`
}

// SessionSiteView is one row of the sign-in sync page. SourceCookies is null
// until the extension has counted the site.
type SessionSiteView struct {
	Domain          string   `json:"domain"`
	Label           string   `json:"label"`
	Cookies         SafeInt  `json:"cookies"`
	SignIn          string   `json:"signIn"`
	SignInCheckedAt *Time    `json:"signInCheckedAt" tstype:"Time | null"`
	SignInURL       string   `json:"signInUrl,omitempty"`
	SourceCookies   *SafeInt `json:"sourceCookies" tstype:"SafeInt | null"`
}

// SessionOutcome says how the last request or undo ended. State is
// succeeded, failed or undone; the message never holds a cookie name or value.
type SessionOutcome struct {
	State   string `json:"state"`
	Message string `json:"message"`
	At      Time   `json:"at"`
}

// SessionImportSummary is the last import's sites, counts and time.
type SessionImportSummary struct {
	At      Time                `json:"at"`
	Domains []string            `json:"domains"`
	Result  SessionImportResult `json:"result"`
}

// SessionsView is the sign-in sync page: the sites list with both cookie
// counts, the extension's last check-in, the pending request and the last
// outcome. It holds counts and times only.
type SessionsView struct {
	Sites         []SessionSiteView     `json:"sites"`
	LastCheckin   *Time                 `json:"lastCheckin" tstype:"Time | null"`
	Pending       *SessionPending       `json:"pending" tstype:"SessionPending | null"`
	LastOutcome   *SessionOutcome       `json:"lastOutcome" tstype:"SessionOutcome | null"`
	LastImport    *SessionImportSummary `json:"lastImport" tstype:"SessionImportSummary | null"`
	UndoAvailable bool                  `json:"undoAvailable"`
}

package wire

// KBGitHubAccount answers GET /api/kb/github and the sign-in and sign-out
// routes: the GitHub account the repository sources are read with. State is
// signed-out, pending, signed-in or error. Source says where the token in use
// comes from: "vault" is the one the app stored, "gh" the user's own gh
// command. A pending sign-in carries the code to enter at VerificationURI. The
// token itself is never in a reply.
type KBGitHubAccount struct {
	State           string `json:"state"`
	Login           string `json:"login"`
	Source          string `json:"source"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri,omitempty"`
	ExpiresAt       *Time  `json:"expiresAt,omitempty"`
	Error           string `json:"error,omitempty"`
}

// KBGitHubSignIn answers POST /api/kb/github/signin: the code to enter at
// github.com/login/device. The server waits for the person itself.
type KBGitHubSignIn struct {
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresAt       Time   `json:"expiresAt"`
}

// KBGitHubRepo is one repository of the signed-in account. AlreadyAdded says
// it is a repository source already.
type KBGitHubRepo struct {
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	PushedAt      string `json:"pushedAt"`
	DefaultBranch string `json:"defaultBranch"`
	Language      string `json:"language"`
	AlreadyAdded  bool   `json:"alreadyAdded"`
}

// KBGitHubRepos is one page of the account's repositories, most recently
// pushed first. More says a further page may exist.
type KBGitHubRepos struct {
	Repos []KBGitHubRepo `json:"repos"`
	Page  SafeInt        `json:"page"`
	More  bool           `json:"more"`
}

// KBPageRef names a wiki page.
type KBPageRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// KBPageLink is a link in a page's text to another page, as written
// (Path is bundle-relative, "/projects/design.md"). ID is empty when no page
// is at that path.
type KBPageLink struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	ID    string `json:"id"`
}

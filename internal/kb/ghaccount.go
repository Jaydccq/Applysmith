package kb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// GitHubClientID is the client id of the owner's "Applysmith" OAuth App on GitHub
// (Device Flow enabled, user tokens that do not expire, since the vault keeps
// one token and nothing refreshes it). A client id is public.
const GitHubClientID = "Ov23limK8iODnIPOKJXd"

const (
	githubOAuth     = "https://github.com"
	ghScope         = "repo"
	ghReposPageSize = 50
	ghMaxListBytes  = 4 << 20
	ghUserTTL       = 5 * time.Minute
)

// Account states.
const (
	GitHubSignedOut = "signed-out"
	GitHubPending   = "pending"
	GitHubSignedIn  = "signed-in"
	GitHubError     = "error"
)

// Where the token in use comes from.
const (
	GitHubSourceVault = "vault"
	GitHubSourceGH    = "gh"
)

var (
	// ErrGitHubSignedOut marks a request that needs an account while none is
	// signed in.
	ErrGitHubSignedOut = errors.New("no GitHub account is signed in")
	// ErrGitHubToken marks a token GitHub refused.
	ErrGitHubToken = errors.New("GitHub did not accept the token")
)

// GitHubAccountOptions are the account's collaborators.
type GitHubAccountOptions struct {
	// Load returns the stored token, "" when none. Save and Forget write it.
	Load   func() string
	Save   func(token string) error
	Forget func() error
	// CLIToken returns the token of the user's gh command, "" when it has
	// none. Nil uses GHCLIToken.
	CLIToken func(context.Context) string
	// Spawn runs the wait for the person on GitHub; nil starts a goroutine.
	Spawn func(func())
}

// GitHubStatus is what the account looks like now.
type GitHubStatus struct {
	State  string
	Login  string
	Source string
	// UserCode, VerificationURI and ExpiresAt describe a sign-in that waits
	// for the person.
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Error           string
}

// GitHubRepo is one repository of the account, for the picker.
type GitHubRepo struct {
	FullName      string
	Private       bool
	Description   string
	PushedAt      string
	DefaultBranch string
	Language      string
	AlreadyAdded  bool
}

// GitHubAccount is the signed-in GitHub account: an OAuth device flow whose
// token is stored through the options, a gh command fallback, and the list of
// the account's repositories. The token never leaves this type except as the
// bearer of a request to GitHub.
type GitHubAccount struct {
	opts     GitHubAccountOptions
	clientID string

	// oauthBase, apiBase and insecure are test seams.
	oauthBase string
	apiBase   string
	insecure  bool

	start sync.Mutex // one sign-in starts at a time

	mu      sync.Mutex
	flow    *deviceFlow
	lastErr string
	user    cachedUser
}

type deviceFlow struct {
	auth   *oauth2.DeviceAuthResponse
	cancel context.CancelFunc
}

// status is the flow as the page shows it: the code to enter and where.
func (f *deviceFlow) status() GitHubStatus {
	return GitHubStatus{State: GitHubPending, UserCode: f.auth.UserCode, VerificationURI: f.auth.VerificationURI, ExpiresAt: f.auth.Expiry}
}

type cachedUser struct {
	token, login string
	at           time.Time
}

// NewGitHubAccount returns the account of this build.
func NewGitHubAccount(opts GitHubAccountOptions) *GitHubAccount {
	if opts.CLIToken == nil {
		opts.CLIToken = GHCLIToken
	}
	if opts.Spawn == nil {
		opts.Spawn = func(f func()) { go f() }
	}
	return &GitHubAccount{opts: opts, clientID: GitHubClientID, oauthBase: githubOAuth, apiBase: githubAPI}
}

func (a *GitHubAccount) oauthConfig() *oauth2.Config {
	return &oauth2.Config{ClientID: a.clientID, Scopes: []string{ghScope}, Endpoint: oauth2.Endpoint{
		DeviceAuthURL: a.oauthBase + "/login/device/code",
		TokenURL:      a.oauthBase + "/login/oauth/access_token",
		AuthStyle:     oauth2.AuthStyleInParams,
	}}
}

// oauthContext carries the HTTP client the OAuth library uses: https to the
// GitHub host only, bounded, with the same redirect rule as the API client.
func (a *GitHubAccount) oauthContext(ctx context.Context) (context.Context, error) {
	g, err := newGitHub(a.oauthBase, "", a.insecure)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, oauth2.HTTPClient, g.client), nil
}

// token returns the token in use and where it came from. The app's own token
// wins over the gh command's.
func (a *GitHubAccount) token(ctx context.Context) (tok, source string) {
	if a.opts.Load != nil {
		if t := strings.TrimSpace(a.opts.Load()); t != "" {
			return t, GitHubSourceVault
		}
	}
	if t := a.opts.CLIToken(ctx); t != "" {
		return t, GitHubSourceGH
	}
	return "", ""
}

// SignIn starts the device flow: GitHub gives a code the person enters at the
// verification address, and the account waits for them in the background,
// under bg, until the code expires or SignOut. A flow already waiting is
// returned as it is. The answer is always a pending flow with its code, also
// when the wait ended before SignIn returned; Status tells how it ended.
func (a *GitHubAccount) SignIn(ctx, bg context.Context) (GitHubStatus, error) {
	a.start.Lock()
	defer a.start.Unlock()
	a.mu.Lock()
	waiting := a.flow
	a.mu.Unlock()
	if waiting != nil {
		return waiting.status(), nil
	}
	cfg := a.oauthConfig()
	hctx, err := a.oauthContext(ctx)
	if err != nil {
		return GitHubStatus{}, err
	}
	da, err := cfg.DeviceAuth(hctx)
	if err != nil {
		return GitHubStatus{}, fmt.Errorf("%w: GitHub did not start the sign-in: %s", ErrUnavailable, flowMessage(err))
	}
	fctx, cancel := context.WithCancel(bg)
	f := &deviceFlow{auth: da, cancel: cancel}
	a.mu.Lock()
	a.flow, a.lastErr = f, ""
	a.mu.Unlock()
	a.opts.Spawn(func() { a.wait(fctx, cfg, f) })
	return f.status(), nil
}

// wait polls GitHub until the person finishes, then stores the token. The OAuth
// library keeps to GitHub's interval and slows down when told to.
func (a *GitHubAccount) wait(ctx context.Context, cfg *oauth2.Config, f *deviceFlow) {
	defer f.cancel()
	var msg string
	hctx, err := a.oauthContext(ctx)
	if err == nil {
		var tok *oauth2.Token
		if tok, err = cfg.DeviceAccessToken(hctx, f.auth); err == nil {
			if a.opts.Save == nil {
				err = errors.New("nowhere to keep the token")
			} else if err = a.opts.Save(tok.AccessToken); err != nil {
				msg = "The sign-in worked but the token could not be stored: " + err.Error()
			}
		}
	}
	if err != nil && msg == "" {
		msg = flowMessage(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.flow != f {
		return // signed out meanwhile
	}
	a.flow, a.lastErr = nil, msg
}

// flowMessage words why a device flow step failed. No token is involved.
func flowMessage(err error) string {
	var re *oauth2.RetrieveError
	switch {
	case errors.As(err, &re) && re.ErrorCode == "access_denied":
		return "The request was cancelled on GitHub. Sign in again to get a new code."
	case errors.As(err, &re) && re.ErrorCode == "device_flow_disabled":
		return "Device Flow is not enabled for this GitHub OAuth App. Turn on Enable Device Flow in the app's settings on GitHub."
	case errors.As(err, &re) && re.ErrorCode == "expired_token", errors.Is(err, context.DeadlineExceeded):
		return "The code expired before it was used. Sign in again to get a new one."
	case errors.As(err, &re) && re.ErrorDescription != "":
		return "GitHub did not complete the sign-in: " + re.ErrorDescription
	case errors.As(err, &re) && re.ErrorCode != "":
		return "GitHub did not complete the sign-in: " + re.ErrorCode
	}
	return "GitHub could not be reached: " + err.Error()
}

// SignOut ends a waiting sign-in and removes the app's own token. The gh
// command stays signed in; Status then reports it.
func (a *GitHubAccount) SignOut() error {
	a.mu.Lock()
	if a.flow != nil {
		a.flow.cancel()
		a.flow = nil
	}
	a.lastErr, a.user = "", cachedUser{}
	a.mu.Unlock()
	if a.opts.Forget == nil {
		return nil
	}
	return a.opts.Forget()
}

// Status reads the account. A signed-in account is confirmed with GitHub, which
// names it.
func (a *GitHubAccount) Status(ctx context.Context) GitHubStatus {
	a.mu.Lock()
	f, last := a.flow, a.lastErr
	a.mu.Unlock()
	if f != nil {
		return f.status()
	}
	tok, source := a.token(ctx)
	if tok == "" {
		if last != "" {
			return GitHubStatus{State: GitHubError, Error: last}
		}
		return GitHubStatus{State: GitHubSignedOut}
	}
	login, err := a.login(ctx, tok)
	if err != nil {
		return GitHubStatus{State: GitHubError, Source: source, Error: err.Error()}
	}
	return GitHubStatus{State: GitHubSignedIn, Login: login, Source: source, Error: last}
}

// login names the account a token belongs to; it is kept for a few minutes.
func (a *GitHubAccount) login(ctx context.Context, tok string) (string, error) {
	a.mu.Lock()
	u := a.user
	a.mu.Unlock()
	if u.token == tok && time.Since(u.at) < ghUserTTL {
		return u.login, nil
	}
	g, err := newGitHub(a.apiBase, tok, a.insecure)
	if err != nil {
		return "", err
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := g.get(ctx, "/user", ghMaxMetaBytes, &out); err != nil {
		return "", accountError(err)
	}
	if out.Login == "" {
		return "", fmt.Errorf("%w: GitHub named no account", ErrGitHubToken)
	}
	a.mu.Lock()
	a.user = cachedUser{token: tok, login: out.Login, at: time.Now()}
	a.mu.Unlock()
	return out.Login, nil
}

// accountError words a failed account request: a refused token reads as one.
func accountError(err error) error {
	if errors.Is(err, ErrRepoUnavailable) {
		return fmt.Errorf("%w. Sign in with GitHub again.", ErrGitHubToken)
	}
	return err
}

// Repos lists one page of the account's repositories, most recently pushed
// first, and marks those already added as sources. more says a further page
// may exist.
func (a *GitHubAccount) Repos(ctx context.Context, page int, added []Repo) (repos []GitHubRepo, more bool, err error) {
	tok, _ := a.token(ctx)
	if tok == "" {
		return nil, false, ErrGitHubSignedOut
	}
	g, err := newGitHub(a.apiBase, tok, a.insecure)
	if err != nil {
		return nil, false, err
	}
	page = max(page, 1)
	q := url.Values{"sort": {"pushed"}, "per_page": {strconv.Itoa(ghReposPageSize)}, "page": {strconv.Itoa(page)}}
	var raw []struct {
		FullName      string  `json:"full_name"`
		Private       bool    `json:"private"`
		Description   *string `json:"description"`
		PushedAt      string  `json:"pushed_at"`
		DefaultBranch string  `json:"default_branch"`
		Language      *string `json:"language"`
	}
	// url.Values would escape the commas of affiliation.
	if err := g.get(ctx, "/user/repos?affiliation=owner,collaborator,organization_member&"+q.Encode(), ghMaxListBytes, &raw); err != nil {
		return nil, false, accountError(err)
	}
	have := map[string]bool{}
	for _, r := range added {
		have[r.Owner+"/"+r.Name] = true
	}
	repos = make([]GitHubRepo, 0, len(raw))
	for _, r := range raw {
		repos = append(repos, GitHubRepo{FullName: r.FullName, Private: r.Private, Description: deref(r.Description), PushedAt: r.PushedAt,
			DefaultBranch: r.DefaultBranch, Language: deref(r.Language), AlreadyAdded: have[strings.ToLower(r.FullName)]})
	}
	return repos, len(raw) == ghReposPageSize, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

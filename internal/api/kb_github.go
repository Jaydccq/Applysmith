package api

import (
	"errors"
	"strconv"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/knowledgeapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/kb"
	"applysmith/internal/vault"
)

// The GitHub account the repository sources are read with. The server owns the
// device flow and the token, so the page only ever sees the code to enter and
// the account's name.

const githubTokenSecret = "github-token"

// newGitHubAccount keeps the token in the vault as github-token, the secret
// the repository ingestion reads, and runs the flow's wait under the server's
// background group.
func (s *Server) newGitHubAccount() *kb.GitHubAccount {
	v := s.cfg.Vault
	return kb.NewGitHubAccount(kb.GitHubAccountOptions{
		Load: func() string {
			tok, _ := v.Get(githubTokenSecret)
			return tok
		},
		Save: func(tok string) error {
			_, err := v.Set(githubTokenSecret, tok)
			return err
		},
		Forget: func() error {
			err := v.Delete(githubTokenSecret)
			var ve *vault.Error
			if errors.As(err, &ve) && ve.Code == vault.CodeNotFound {
				return nil
			}
			return err
		},
		Spawn: func(f func()) {
			s.bgWG.Add(1)
			go func() {
				defer s.bgWG.Done()
				f()
			}()
		},
	})
}

func (s *Server) knowledge() (*kb.Service, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.kb, nil
}

func (s *Server) toKBGitHubAccount(st kb.GitHubStatus) wire.KBGitHubAccount {
	out := wire.KBGitHubAccount{State: st.State, Login: st.Login, Source: st.Source,
		UserCode: st.UserCode, VerificationURI: st.VerificationURI, Error: st.Error}
	if !st.ExpiresAt.IsZero() {
		out.ExpiresAt = &wire.Time{Time: st.ExpiresAt}
	}
	return out
}

func (s *Server) kbGitHubStatus(c *apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: s.toKBGitHubAccount(s.github.Status(c.R.Context()))}, nil
}

func (s *Server) kbGitHubSignIn(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.github.SignIn(c.R.Context(), s.bg)
	if err != nil {
		return apikit.Reply{}, knowledgeapi.KBError(err)
	}
	return apikit.Reply{Data: wire.KBGitHubSignIn{UserCode: st.UserCode, VerificationURI: st.VerificationURI, ExpiresAt: wire.Time{Time: st.ExpiresAt}}}, nil
}

// kbGitHubSignOut removes the app's own token and ends a waiting sign-in. The
// gh command stays signed in: the reply says so, through its source.
func (s *Server) kbGitHubSignOut(c *apikit.Call) (apikit.Reply, error) {
	if err := s.github.SignOut(); err != nil {
		return apikit.Reply{}, vaultError(err)
	}
	return s.kbGitHubStatus(c)
}

func (s *Server) kbGitHubRepos(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	page := 1
	if p := c.R.URL.Query().Get("page"); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 1000 {
			return apikit.Reply{}, apikit.InvalidField("page", "a page is a number from 1 to 1000")
		}
		page = n
	}
	added, err := svc.ListRepos(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	repos, more, err := s.github.Repos(c.R.Context(), page, added)
	switch {
	case errors.Is(err, kb.ErrGitHubSignedOut), errors.Is(err, kb.ErrGitHubToken):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case err != nil:
		return apikit.Reply{}, knowledgeapi.KBError(err)
	}
	out := wire.KBGitHubRepos{Repos: make([]wire.KBGitHubRepo, 0, len(repos)), Page: apikit.Safe64(int64(page)), More: more}
	for _, r := range repos {
		out.Repos = append(out.Repos, wire.KBGitHubRepo{FullName: r.FullName, Private: r.Private, Description: r.Description,
			PushedAt: r.PushedAt, DefaultBranch: r.DefaultBranch, Language: r.Language, AlreadyAdded: r.AlreadyAdded})
	}
	return apikit.Reply{Data: out}, nil
}

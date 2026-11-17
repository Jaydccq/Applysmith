package gmail

import (
	"context"
	"fmt"
	"time"
)

// Service is the entry point the CLI, the API and the apply chain use. Each
// capability lives in its own file; the fields are the shared collaborators.
type Service struct {
	Client  *Client
	Account *Account
	Repo    *Repo
	Now     func() time.Time
	// Demo reports a demo workspace, where the connection test counts message
	// ids and reads no message.
	Demo func() bool
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Test proves the connection: it signs in, opens All Mail and counts the
// messages of the last day, reading the ids of the newest five. A demo
// workspace counts and reads nothing.
func (s *Service) Test(ctx context.Context) (int, error) {
	ctx = withDemoConnect(ctx)
	sess, err := s.Client.open(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer sess.close()
	uids, err := sess.searchRaw(fmt.Sprintf("after:%d", s.now().Add(-24*time.Hour).Unix()))
	if err != nil {
		return 0, ctxErr(ctx, err)
	}
	if s.Demo != nil && s.Demo() {
		return len(uids), nil
	}
	if _, err := sess.ids(uids[max(len(uids)-5, 0):]); err != nil {
		return 0, ctxErr(ctx, err)
	}
	return len(uids), nil
}

// ConnectResult is a connection Gmail accepted and the workspace stored.
type ConnectResult struct {
	Mailbox string
	// Previous is the mailbox connected before when Mailbox is another
	// account: syncing starts again for the new one.
	Previous string
}

// Connect signs in to Gmail with an address and its app password, opens All
// Mail, and only then stores them: a password Gmail refuses is never kept.
func (s *Service) Connect(ctx context.Context, address, password string) (ConnectResult, error) {
	c, err := cleanCredentials(address, password)
	if err != nil {
		return ConnectResult{}, err
	}
	ctx = withDemoConnect(ctx)
	sess, err := s.Client.open(ctx, &c)
	if err != nil {
		return ConnectResult{}, err
	}
	sess.close()
	prev, err := s.Account.save(ctx, c)
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{Mailbox: c.Address, Previous: prev}, nil
}

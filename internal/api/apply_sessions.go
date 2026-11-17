package api

import (
	"context"

	"applysmith/internal/apply"
)

// ownHostSessions lets an apply run ask the user's everyday Chrome for the
// sign-in of the host it is applying to: it is the chain's own call to
// SyncOwnHost, before any password is tried.
type ownHostSessions struct{ s *Server }

// ApplySessions returns the apply chain's view of the session import.
func (s *Server) ApplySessions() apply.SessionSync { return ownHostSessions{s} }

func (o ownHostSessions) SyncOwnHost(ctx context.Context, host string) (apply.OwnHostResult, error) {
	got, err := o.s.SyncOwnHost(ctx, host)
	if err != nil {
		return apply.OwnHostResult{}, err
	}
	res := apply.OwnHostResult{Imported: got.Status == OwnHostImported}
	if !res.Imported {
		res.Reason = got.Reason
		if res.Reason == "" {
			res.Reason = got.Status
		}
	}
	return res, nil
}

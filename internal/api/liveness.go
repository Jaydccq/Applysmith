package api

import (
	"context"
	"fmt"
	"path/filepath"

	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

// livenessChecker builds the checker over the browser reader this server was
// given; nil when there is none.
func (s *Server) livenessChecker(d *domain) *discovery.Checker {
	if s.cfg.Domain.Liveness == nil {
		return nil
	}
	return &discovery.Checker{Repo: d.repo, Reader: s.cfg.Domain.Liveness, Tracker: d.tracker}
}

// livenessAudit is the body of the scheduled liveness.audit job; nil leaves
// the kind unregistered.
func (s *Server) livenessAudit(d *domain) func(context.Context) (string, error) {
	c := s.livenessChecker(d)
	if c == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		res, err := c.Audit(ctx)
		summary := fmt.Sprintf("%d checked, %d expired, %d mismatch, %d uncertain, %d skipped as recent, %d failed", res.Checked, res.Expired, res.Mismatch, res.Uncertain, res.Skipped, res.Failed)
		return summary, err
	}
}

// registryRefresh is the body of the scheduled registry.refresh job.
func (s *Server) registryRefresh(d *domain) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		portals, e := loadPortals(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		seeder := &discovery.Seeder{Repo: d.repo, Dir: filepath.Join(s.cfg.Workspace.StateDir(), "downloads"), HTTP: s.cfg.Domain.ScanHTTP}
		scanner := s.newScanner(d, workspace.Profile{}, nil)
		res, err := discovery.RegistryRefresh(ctx, seeder, scanner, portals, nil)
		return fmt.Sprintf("%d new boards, %d dead boards probed, %d revived", res.New(), res.Probe.Probed, res.Probe.Revived), err
	}
}

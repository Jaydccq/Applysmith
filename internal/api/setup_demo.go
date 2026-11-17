package api

import (
	"net/http"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/demo"
)

// setupStatus says whether this server's workspace is the demo; every page
// reads it for the demo banner.
func (s *Server) setupStatus(*apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: wire.SetupStatus{Demo: s.cfg.Workspace.IsDemo()}}, nil
}

// setupDemo runs the demo init for the Setup page's "Try the demo".
func (s *Server) setupDemo(c *apikit.Call) (apikit.Reply, error) {
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := s.cfg.Setup.Demo(c.R.Context(), h)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: res}, nil
}

// demoSampleApplication serves the demo's sample application form, which only
// a demo workspace has. The page posts nowhere; its Submit shows a message.
func (s *Server) demoSampleApplication(c *apikit.Call) (apikit.Reply, error) {
	if !s.cfg.Workspace.IsDemo() {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "the sample application form exists only in the demo workspace")
	}
	h := c.W.Header()
	h.Set("Content-Security-Policy", demo.SampleApplicationCSP)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(demo.SampleApplication())
	c.Handled = true
	return apikit.Reply{}, nil
}

package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/remote"
	"applysmith/internal/remote/qr"
	"applysmith/internal/remote/tunnel"
)

// reasonTunnelDown is the reason of the unavailable error that an offer gets
// while no https tunnel address exists.
const reasonTunnelDown = "tunnel_not_running"

func deviceID(p apikit.Principal) string { return strings.TrimPrefix(p.ID, deviceIDPrefix) }

func (s *Server) remoteStatus(c *apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: s.remote.status(c.R.Context())}, nil
}

// createRemoteOffer makes a pairing offer for the tunnel's public address. An
// offer is made only while the tunnel runs behind https, so a code never
// travels over plaintext.
func (s *Server) createRemoteOffer(c *apikit.Call) (apikit.Reply, error) {
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	base, ok := s.remote.publicBase()
	if !ok {
		return apikit.Reply{}, apikit.WithReason(apikit.Unavailable("remote access is off or the tunnel is not running; start it, then pair a device"), reasonTunnelDown)
	}
	offer, err := devs.CreateOffer()
	if err != nil {
		return apikit.Reply{}, err
	}
	link := base + "/pair#" + offer.Code
	modules, err := qr.Encode(link)
	if err != nil {
		return apikit.Reply{}, apikit.NewErr(wire.CodeInternal, "the pairing link does not fit a QR code")
	}
	s.remote.record(remote.EventPairOffer, string(c.P.Kind), "offer created")
	return apikit.Reply{Status: http.StatusCreated, Data: wire.RemoteOffer{Link: link, ExpiresAt: wire.Time{Time: offer.ExpiresAt.UTC()},
		QR: wire.RemoteQR{Size: wire.SafeInt(len(modules)), Rows: qr.Rows(modules)}}}, nil
}

// claimRemotePair redeems an offer over the tunnel and issues the device
// token, once, in the body and in the device cookie.
func (s *Server) claimRemotePair(c *apikit.Call) (apikit.Reply, error) {
	if !requestIsRemote(c.R) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "a device is paired over the tunnel")
	}
	var req wire.RemotePairClaim
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	src := s.source(c.R)
	dev, token, err := devs.Claim(c.R.Context(), req.Code, req.Label)
	switch {
	case errors.Is(err, remote.ErrInvalidCode):
		s.remote.record(remote.EventPairFailed, src, "the pairing code was not valid")
		return apikit.Reply{}, apikit.NewErr(wire.CodeUnauthenticated, "the pairing code is not valid")
	case errors.Is(err, remote.ErrLabel):
		return apikit.Reply{}, apikit.InvalidField("label", "a device label has 1 to %d printable characters", remote.MaxLabel)
	case errors.Is(err, remote.ErrDeviceLimit):
		s.remote.record(remote.EventDeviceLimit, src, "a claim was refused at the device limit")
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", err.Error()), wire.ReasonDeviceLimit)
	case err != nil:
		return apikit.Reply{}, err
	}
	// The redirect uses the configured origin: behind the tunnel the address
	// the server is bound to is not one the phone can reach.
	base, _ := s.remote.publicBase()
	if base == "" {
		base = "https://" + c.R.Host
	}
	s.remote.recordDevice(remote.EventPairClaim, dev.ID, src, "paired "+dev.Label)
	if ns, err := s.notifications(); err == nil {
		ns.desktop.Text(c.R.Context(), "New device paired", dev.Label, "/settings#settings-remote")
	}
	s.notify("notifications")
	http.SetCookie(c.W, deviceCookie(token, int(400*24*time.Hour/time.Second)))
	c.W.Header().Set("Cache-Control", "no-store")
	return apikit.Reply{Status: http.StatusCreated, Data: wire.RemoteClaimed{DeviceID: dev.ID, Label: dev.Label, Token: token, Redirect: base + "/"}}, nil
}

func (s *Server) remoteDevices(c *apikit.Call) (apikit.Reply, error) {
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	list := devs.List()
	out := wire.RemoteDeviceList{Devices: make([]wire.RemoteDevice, len(list))}
	for i, d := range list {
		seen := wire.Time{Time: d.LastSeen.UTC()}
		out.Devices[i] = wire.RemoteDevice{ID: d.ID, Label: d.Label, CreatedAt: wire.Time{Time: d.CreatedAt.UTC()}, LastSeenAt: &seen,
			Network: d.Network, Connected: s.remote.connected(devs, d)}
	}
	return apikit.Reply{Data: out}, nil
}

// afterRevoke closes the streams the revoked devices held and records it.
func (s *Server) afterRevoke(c *apikit.Call, ids []string, detail string) {
	for _, id := range ids {
		s.streams.revokeID(deviceIDPrefix+id, errStreamRevoked)
		s.remote.recordDevice(remote.EventRevoke, id, string(c.P.Kind), detail)
	}
	s.notify("notifications")
}

func (s *Server) revokeRemoteDevice(c *apikit.Call) (apikit.Reply, error) {
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	ok, err := devs.Revoke(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such device")
	}
	s.afterRevoke(c, []string{id}, "revoked")
	return apikit.Reply{Data: wire.RemoteRevoked{Revoked: 1}}, nil
}

func (s *Server) revokeAllRemoteDevices(c *apikit.Call) (apikit.Reply, error) {
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	ids, err := devs.RevokeAll(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	s.afterRevoke(c, ids, "revoked")
	return apikit.Reply{Data: wire.RemoteRevoked{Revoked: wire.SafeInt(len(ids))}}, nil
}

func (s *Server) remoteMe(c *apikit.Call) (apikit.Reply, error) {
	me := wire.RemoteMe{Kind: string(c.P.Kind)}
	if c.P.Kind == apikit.PrincipalRemoteDevice {
		me.DeviceID = deviceID(c.P)
		if devs, err := s.remote.service(); err == nil {
			for _, d := range devs.List() {
				if d.ID == me.DeviceID {
					me.Label = d.Label
				}
			}
		}
	}
	return apikit.Reply{Data: me}, nil
}

func (s *Server) retryRemote(c *apikit.Call) (apikit.Reply, error) {
	s.remote.mu.Lock()
	tun, active := s.remote.tun, s.remote.active
	s.remote.mu.Unlock()
	if !active || tun == nil {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "remote access is off; there is no tunnel to restart")
	}
	if m := tun.Status().Mode; m != tunnel.StateError && m != tunnel.StateFailed {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the tunnel is %s; only a tunnel in error or failed is restarted", m)
	}
	tun.Retry()
	return apikit.Reply{Status: http.StatusAccepted, Data: s.remote.status(c.R.Context())}, nil
}

func (s *Server) remoteEvents(c *apikit.Call) (apikit.Reply, error) {
	devs, err := s.remote.service()
	if err != nil {
		return apikit.Reply{}, err
	}
	evs, err := devs.Events(c.R.Context(), remoteEventsList)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.RemoteEventList{Events: make([]wire.RemoteEvent, len(evs))}
	for i, e := range evs {
		out.Events[i] = wire.RemoteEvent{At: wire.Time{Time: e.At}, Kind: e.Kind, DeviceID: e.DeviceID, Source: e.Source, Detail: e.Detail}
	}
	return apikit.Reply{Data: out}, nil
}

// remoteOwns reports whether the remote device of the call started the
// conversation.
func (s *Server) remoteOwns(c *apikit.Call, sessionID string) bool {
	devs, err := s.remote.service()
	return err == nil && devs.OwnsSession(c.R.Context(), sessionID, deviceID(c.P))
}

// remoteOwn records the conversation a remote device just started.
func (s *Server) remoteOwn(c *apikit.Call, sessionID string) error {
	devs, err := s.remote.service()
	if err != nil {
		return err
	}
	return devs.OwnSession(c.R.Context(), sessionID, deviceID(c.P))
}

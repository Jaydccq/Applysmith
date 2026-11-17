package api

// remoteRoutes serve remote access. The admin routes manage the mode's state,
// the pairing offers and the devices from this computer. The rest are the
// routes a remote device may call: its own identity, revoking itself, and the
// pairing page with the claim it posts.
//
// The claim is the one public route a remote caller may use that issues a
// credential, so it cannot carry the secret flag the route table forbids on a
// remote route: the one-time code is the proof, it is limited to five attempts
// a minute per source, and it works only over the tunnel.
var remoteRoutes = []Route{
	route("GET", "/api/remote/status", func(r *Route) { reads("RemoteStatus")(r); r.Access = AccessAdmin }),
	route("POST", "/api/remote/offers", func(r *Route) { reads("RemoteOffer")(r); r.Access, r.Secret = AccessAdmin, true }),
	route("GET", "/api/remote/devices", func(r *Route) { reads("RemoteDeviceList")(r); r.Access = AccessAdmin }),
	route("DELETE", "/api/remote/devices/{id}", func(r *Route) { reads("RemoteRevoked")(r); r.Access = AccessAdmin }),
	route("POST", "/api/remote/devices/revoke-all", func(r *Route) { reads("RemoteRevoked")(r); r.Access = AccessAdmin }),
	route("POST", "/api/remote/retry", func(r *Route) { reads("RemoteStatus")(r); r.Access = AccessAdmin }),
	route("GET", "/api/remote/events", func(r *Route) { reads("RemoteEventList")(r); r.Access = AccessAdmin }),
	route("GET", "/api/remote/me", remoteReads("RemoteMe")),
	route("POST", "/api/remote/pair/claim", func(r *Route) {
		sends("RemotePairClaim", "RemoteClaimed")(r)
		r.Access, r.Remote, r.Limit = AccessPublic, RemoteAllowed, LimitPairing
	}),
	route("GET", "/pair", func(r *Route) { reads(PayloadAsset)(r); r.Access, r.Remote = AccessPublic, RemoteAllowed }),
}

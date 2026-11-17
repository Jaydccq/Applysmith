package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/remote"
	"applysmith/internal/remote/qr"
	"applysmith/internal/workspace"
)

const remoteUsage = `usage:
  applysmith remote status [--json]
  applysmith remote on tunnel [--yes]
  applysmith remote off
  applysmith remote retry
  applysmith remote pair
  applysmith remote devices [--json]
  applysmith remote revoke <id>|all

on and off edit policy.yml directly, keep its comments and need no running server; a running server applies the change within 5 seconds
the other commands talk to the running server and say so when there is none
every command takes --workspace <dir>

` + remoteIngress

const remoteIngress = `Remote access requires a named Cloudflare Tunnel and an HTTPS public hostname.
Set remote_access.tunnel.public_url in policy.yml and store cloudflared-tunnel-token in the keychain
(or set APPLYSMITH_SECRET_CLOUDFLARED_TUNNEL_TOKEN). Quick Tunnels do not support live updates.
In the Cloudflare dashboard, point its public hostname at http://127.0.0.1:<tunnel_port> (47321 unless policy.yml says otherwise), never at the
dashboard port, which refuses what a tunnel forwards.
`

const remoteWarning = `Remote access over an HTTPS tunnel makes this dashboard reachable from the internet.
A device that pairs with a one-time code can read your job search (tracker, reports, runs, schedules)
and chat with the agent in read and draft mode; it cannot change settings, read secrets or apply.
It needs cloudflared, which you install yourself; Applysmith never downloads it.
` + remoteIngress

func (e *env) remoteCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, remoteUsage)
		return 2
	}
	name := "remote " + args[0]
	fs, parse := e.subFlags("remote", args[0], remoteUsage)
	wsFlag := fs.String("workspace", "", "workspace directory")
	yes := fs.Bool("yes", false, "confirm turning remote access on")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, ok := parse(args[1:])
	if !ok {
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail(name, err)
	}
	switch args[0] {
	case "on":
		return e.remoteOn(ws, pos, *yes)
	case "off":
		if len(pos) > 0 {
			fmt.Fprint(e.stderr, remoteUsage)
			return 2
		}
		return e.remoteSetMode(ws, "remote off", "off", "remote access is off")
	case "status", "retry", "pair", "devices", "revoke":
	default:
		fmt.Fprint(e.stderr, remoteUsage)
		return 2
	}
	return e.remoteLive(ctx, ws, args[0], pos, *asJSON)
}

// remoteOn asks for the confirmation, then writes the mode. Only the tunnel
// exists: LAN access is not supported.
func (e *env) remoteOn(ws *workspace.Workspace, pos []string, yes bool) int {
	switch {
	case len(pos) == 1 && pos[0] == "lan":
		fmt.Fprintln(e.stderr, "applysmith remote on: LAN mode is not supported; remote access uses the HTTPS tunnel (`applysmith remote on tunnel`)")
		return 2
	case len(pos) != 1 || pos[0] != "tunnel":
		fmt.Fprint(e.stderr, remoteUsage)
		return 2
	}
	if !yes {
		if !e.isTerminal() {
			fmt.Fprint(e.stderr, "applysmith remote on: this needs your confirmation. Run it in a terminal, or pass --yes to confirm that:\n\n"+remoteWarning)
			return 3
		}
		fmt.Fprint(e.stdout, remoteWarning+"\nTurn remote access on? [y/N] ")
		line, err := bufio.NewReader(e.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return e.fail("remote on", errors.New("the answer could not be read; nothing was changed"))
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(e.stdout, "nothing was changed")
			return 3
		}
	}
	return e.remoteSetMode(ws, "remote on", "tunnel", "remote access is on (tunnel)")
}

// remoteSetMode edits remote_access.mode in policy.yml in place.
func (e *env) remoteSetMode(ws *workspace.Workspace, name, mode, done string) int {
	cur, err := ws.Read(workspace.FilePolicy)
	if err != nil {
		return e.fail(name, err)
	}
	if cur.Revision == workspace.RevisionAbsent {
		return e.fail(name, errors.New("this workspace has no policy.yml; run `applysmith init` first"))
	}
	if _, err := ws.Edit(workspace.FilePolicy, []workspace.Op{{Kind: workspace.OpSet, Path: "remote_access.mode", Value: mode}}, cur.Revision); err != nil {
		return e.fail(name, err)
	}
	fmt.Fprintf(e.stdout, "policy.yml now holds remote_access.mode: %s: %s\n", mode, done)
	if held, _ := api.LockHeld(ws.StateDir()); held {
		fmt.Fprintln(e.stdout, "The running server applies it within 5 seconds.")
	} else if mode != "off" {
		fmt.Fprintln(e.stdout, "Start the app or `applysmith serve` to open the tunnel.")
	}
	return 0
}

// remoteLive runs the commands that need the server.
func (e *env) remoteLive(ctx context.Context, ws *workspace.Workspace, cmd string, pos []string, asJSON bool) int {
	name := "remote " + cmd
	if (cmd == "revoke") != (len(pos) == 1) || (cmd != "revoke" && len(pos) > 0) {
		fmt.Fprint(e.stderr, remoteUsage)
		return 2
	}
	held, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return e.fail(name, err)
	}
	if !held {
		return e.fail(name, errNoServer)
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail(name, err)
	}
	switch cmd {
	case "status":
		var st wire.RemoteStatus
		if err := e.liveCall(ctx, srv, http.MethodGet, "/api/remote/status", &st); err != nil {
			return e.fail(name, err)
		}
		if asJSON {
			return e.printJSON(st)
		}
		e.printRemoteStatus(st)
	case "retry":
		var st wire.RemoteStatus
		if err := e.liveCall(ctx, srv, http.MethodPost, "/api/remote/retry", &st); err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintf(e.stdout, "restarting the tunnel (it was %s)\n", st.Tunnel.State)
	case "pair":
		var offer wire.RemoteOffer
		if err := e.liveCall(ctx, srv, http.MethodPost, "/api/remote/offers", &offer); err != nil {
			return e.fail(name, err)
		}
		e.printOffer(offer)
	case "devices":
		var list wire.RemoteDeviceList
		if err := e.liveCall(ctx, srv, http.MethodGet, "/api/remote/devices", &list); err != nil {
			return e.fail(name, err)
		}
		if asJSON {
			return e.printJSON(list)
		}
		e.printDevices(list)
	case "revoke":
		path, target := "/api/remote/devices/revoke-all", "every device"
		if pos[0] != "all" {
			path, target = "/api/remote/devices/"+url.PathEscape(pos[0]), "device "+pos[0]
		}
		method := http.MethodPost
		if pos[0] != "all" {
			method = http.MethodDelete
		}
		var out wire.RemoteRevoked
		if err := e.liveCall(ctx, srv, method, path, &out); err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintf(e.stdout, "revoked %s (%d); open streams are closed\n", target, out.Revoked)
	}
	return 0
}

func (e *env) printRemoteStatus(st wire.RemoteStatus) {
	fmt.Fprintf(e.stdout, "mode: %s\n", st.Mode)
	for _, l := range st.Listeners {
		fmt.Fprintf(e.stdout, "listener %s: %s\n", l.Name, l.Address)
	}
	fmt.Fprintf(e.stdout, "tunnel: %s\n", st.Tunnel.State)
	if st.Tunnel.PublicURL != "" {
		fmt.Fprintf(e.stdout, "public address: %s\n", st.Tunnel.PublicURL)
	}
	if st.Tunnel.Error != "" {
		fmt.Fprintf(e.stdout, "tunnel problem: %s\n", st.Tunnel.Error)
	}
	if st.Error != "" {
		fmt.Fprintf(e.stdout, "problem: %s\n", st.Error)
	}
	fmt.Fprintf(e.stdout, "devices: %d paired, %d connected\n", st.Devices, st.Connected)
}

// printOffer prints the link and a text QR code of it.
func (e *env) printOffer(o wire.RemoteOffer) {
	modules := make([][]bool, len(o.QR.Rows))
	for y, row := range o.QR.Rows {
		modules[y] = make([]bool, len(row))
		for x := range row {
			modules[y][x] = row[x] == '1'
		}
	}
	fmt.Fprintln(e.stdout, qr.Text(modules))
	fmt.Fprintf(e.stdout, "Open this link on the phone, or scan the code above:\n%s\n", o.Link)
	fmt.Fprintf(e.stdout, "It works once and expires in %d minutes (at %s).\n", int(remote.OfferTTL/time.Minute), o.ExpiresAt.Local().Format("15:04"))
}

func (e *env) printDevices(list wire.RemoteDeviceList) {
	if len(list.Devices) == 0 {
		fmt.Fprintln(e.stdout, "no devices are paired")
		return
	}
	w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLABEL\tPAIRED\tLAST SEEN\tCONNECTED")
	for _, d := range list.Devices {
		seen := "never"
		if d.LastSeenAt != nil {
			seen = d.LastSeenAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", d.ID, d.Label, d.CreatedAt.Local().Format("2006-01-02 15:04"), seen, d.Connected)
	}
	_ = w.Flush()
}

package mcpserver

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const lockFileName = "server.lock"

// Locator finds the API of one workspace and the token that opens it. It reads
// the lock file and the stored token, nothing else.
type Locator struct {
	Workspace *workspace.Workspace
	Backend   vault.Backend
	Getenv    func(string) string
}

// Origin is the loopback address of the workspace's server: the port its lock
// file names. The lock file is writable by anyone with the workspace, so only
// its port is used, never a host. With no port there is no server to call: no
// default is guessed, because another program (the original app's bridge uses
// 47319) may hold any fixed port and would receive the token.
func (l Locator) Origin() (string, error) {
	port, found := l.Port()
	if !found {
		return "", newToolError(CodeServerNotRunning, remedyStart, "no Applysmith server runs for this workspace")
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

// Port is the port the lock file names; found is false when the file does not
// name one.
func (l Locator) Port() (port int, found bool) {
	b, err := os.ReadFile(filepath.Join(l.Workspace.StateDir(), lockFileName))
	if err != nil {
		return 0, false
	}
	var doc struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(b, &doc) != nil || !validPort(doc.Port) {
		return 0, false
	}
	return doc.Port, true
}

func validPort(p int) bool { return p > 0 && p < 65536 }

// Token reads the API token: the environment override, else the keychain
// entry of the workspace. Each call reads it afresh; the client keeps the
// answer and asks again only after the API refuses it.
func (l Locator) Token() (string, error) {
	getenv := l.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := strings.TrimSpace(getenv(vault.EnvName(vault.APIToken))); v != "" {
		return v, nil
	}
	id, ok, err := l.Workspace.ExistingID()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNoToken
	}
	v, err := vault.New(id, l.Backend, vault.WithGetenv(getenv))
	if err != nil {
		return "", err
	}
	tok, err := v.Get(vault.APIToken)
	if vault.IsCode(err, vault.CodeNotFound) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", errors.New("the keychain entry could not be read")
	}
	return tok, nil
}

// Client builds the API client for this workspace.
func (l Locator) Client() *Client {
	return NewClient(Options{Origin: l.Origin, Token: l.Token, WorkspaceKey: workspace.PathKey(l.Workspace.Root)})
}

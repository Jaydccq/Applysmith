package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"applysmith/internal/api/apikit"
)

// pairPageScript reads the pairing code from the link's fragment, which a
// server never sees, drops it from the address bar and posts it with a label.
// It is inline and allowed by its hash, so the page needs no second route.
const pairPageScript = `(function () {
  var code = location.hash.replace(/^#/, "");
  if (code) history.replaceState(null, "", location.pathname);
  var form = document.getElementById("pair"), note = document.getElementById("note");
  var label = document.getElementById("label");
  label.value = (navigator.platform || "Device").slice(0, 40);
  if (!code) { note.textContent = "Open the pairing link or scan the QR code shown in Applysmith on your computer."; form.hidden = true; return; }
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    note.textContent = "Pairing";
    fetch("/api/remote/pair/claim", {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: JSON.stringify({ code: code, label: label.value })
    }).then(function (res) { return res.json(); }).then(function (body) {
      if (body && body.ok) { location.replace(body.data.redirect); return; }
      note.textContent = body && body.error ? body.error.message : "Pairing failed.";
    }).catch(function () { note.textContent = "The server could not be reached."; });
  });
})();`

const pairPageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Pair this device - Applysmith</title>
<style>body{font:16px system-ui,sans-serif;margin:0;padding:24px;max-width:28rem}input,button{font:inherit;padding:.5rem;margin:.25rem 0;width:100%;box-sizing:border-box}</style>
</head><body><main><h1>Pair this device</h1>
<p>This links this browser to Applysmith on your computer. It can read your job search and answer in chat; it cannot change settings or apply.</p>
<form id="pair"><label for="label">Name this device</label><input id="label" maxlength="40" autocomplete="off"><button type="submit">Pair</button></form>
<p id="note" role="status"></p></main><script>` + pairPageScript + `</script></body></html>`

var pairPageCSP = func() string {
	sum := sha256.Sum256([]byte(pairPageScript))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// pairPage serves the static pairing page. It holds no secret and reads the
// code from the fragment, so the request line never carries it.
func (s *Server) pairPage(c *apikit.Call) (apikit.Reply, error) {
	h := c.W.Header()
	h.Set("Content-Security-Policy", pairPageCSP)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	c.W.WriteHeader(http.StatusOK)
	_, _ = strings.NewReader(pairPageHTML).WriteTo(c.W)
	c.Handled = true
	return apikit.Reply{}, nil
}

//go:build browser

package browser

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// fixtures serves synthetic pages from two loopback origins. Hostnames under
// .test map to loopback through the launch option used only by tests, which
// makes a.example.test and b.sitetwo.test different sites (two registrable
// domains, and so different processes) while 127.0.0.1 and localhost give two
// more.
type fixtures struct {
	a, b *httptest.Server
	mu   sync.Mutex
	reqs []recorded

	holdArrivals atomic.Int64 // /hold requests that arrived since the test reset it
	holdAlone    atomic.Int64 // /hold requests answered without a second one having arrived
}

type recorded struct {
	Host, Path, Method, Origin, Body string
}

func newFixtures() *fixtures {
	f := &fixtures{}
	f.a = f.listen()
	f.b = f.listen()
	return f
}

func (f *fixtures) listen() *httptest.Server {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	s.Listener = l
	s.Start()
	return s
}

func (f *fixtures) close() { f.a.Close(); f.b.Close() }

func port(s *httptest.Server) int { return s.Listener.Addr().(*net.TCPAddr).Port }

// URL helpers. A is a.example.test, B is b.sitetwo.test, Loopback is the
// plain 127.0.0.1 origin of server A (a secure context).
func (f *fixtures) A(path string) string {
	return fmt.Sprintf("http://a.example.test:%d%s", port(f.a), path)
}
func (f *fixtures) B(path string) string {
	return fmt.Sprintf("http://b.sitetwo.test:%d%s", port(f.b), path)
}
func (f *fixtures) Loopback(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port(f.a), path)
}

// Tracker is a path on a host the scan-lane policy treats as a tracker, mapped
// to server A by the launch option used only in tests.
func (f *fixtures) Tracker(path string) string {
	return fmt.Sprintf("http://x.googletagmanager.com:%d%s", port(f.a), path)
}

func (f *fixtures) Localhost(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", port(f.a), path)
}

func (f *fixtures) requests(path string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.reqs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fixtures) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Host: r.Host, Path: r.URL.Path, Method: r.Method, Origin: r.Header.Get("Origin"), Body: string(body)})
	f.mu.Unlock()
	if key, found := strings.CutPrefix(r.URL.Path, "/policy/"); found {
		servePage(w, `<title>policy</title><link rel=stylesheet href="/s-`+key+`.css"><img id=pic src="/pic-`+key+`.png"><script src="/app-`+key+`.js"></script><script src="`+f.Tracker("/t-"+key+".js")+`"></script>`)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/pic-"):
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
		return
	case strings.HasPrefix(r.URL.Path, "/s-"):
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte("body{margin:0}"))
		return
	case strings.HasPrefix(r.URL.Path, "/app-"), strings.HasPrefix(r.URL.Path, "/t-"):
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte("window.__ran=1"))
		return
	}
	switch r.URL.Path {
	case "/hold":
		// Answers once a second request has arrived too, or after 3 s: two
		// operations that overlap are seen by the server together.
		f.holdArrivals.Add(1)
		for end := time.Now().Add(3 * time.Second); f.holdArrivals.Load() < 2 && time.Now().Before(end); {
			time.Sleep(20 * time.Millisecond)
		}
		if f.holdArrivals.Load() < 2 {
			f.holdAlone.Add(1)
		}
		servePage(w, `<title>held</title><p id=p>held</p>`)
	case "/console-code":
		servePage(w, `<title>careers</title><script>console.log("application code", "🌳🍀🌱");
setTimeout(() => console.info({step: 2}), 50);</script>`)
	case "/probe":
		servePage(w, `<title>probe</title><pre id=o></pre><script>
document.getElementById('o').textContent=JSON.stringify({webdriver:navigator.webdriver,ua:navigator.userAgent});</script>`)
	case "/outer":
		servePage(w, `<title>outer</title><h1 id=top>top</h1><iframe id=f src="`+f.B("/inner")+`" width=500 height=300></iframe>`)
	case "/outer-same":
		servePage(w, `<title>outer</title><h1 id=top>top</h1><iframe id=f src="/inner" width=500 height=300></iframe>`)
	case "/inner":
		servePage(w, `<title>inner</title><form onsubmit="return false"><input id=name><input id=pw type=password>
<button id=go type=button>go</button><input id=file type=file></form><div id=res></div><script>
window.__events=[];document.getElementById('go').addEventListener('click',e=>{window.__events.push({click:true,trusted:e.isTrusted});});
document.getElementById('name').addEventListener('keydown',e=>{window.__events.push({key:e.key,trusted:e.isTrusted});});
document.getElementById('file').addEventListener('change',e=>{const f=e.target.files[0];window.__file=f?{name:f.name,size:f.size,type:f.type,trusted:e.isTrusted}:null;});
</script>`)
	case "/clicks":
		servePage(w, `<title>clicks</title><button id=b style="position:absolute;left:50px;top:60px;width:120px;height:40px">b</button><input id=t><script>
window.__events=[];document.getElementById('b').addEventListener('click',e=>window.__events.push({click:true,trusted:e.isTrusted,x:e.clientX,y:e.clientY}));
document.getElementById('t').addEventListener('keydown',e=>window.__events.push({key:e.key,trusted:e.isTrusted}));</script>`)
	case "/api/apply":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"id":"app-1"}`))
	case "/api/whoami":
		c, err := r.Cookie("sid")
		if err != nil {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"no session"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Example", "yes")
		_, _ = w.Write([]byte(`{"user":"` + c.Value + `"}`))
	case "/api/big":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", 250_000)))
	case "/api/refuse":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":"missing field: email"}`))
	case "/submit":
		servePage(w, `<title>submit</title><form id=f onsubmit="return false"><input id=email></form><script>
window.send=async(p)=>{const r=await fetch(p,{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer example-token'},body:JSON.stringify({a:1})});return r.status}</script>`)
	case "/render":
		servePage(w, `<title>render</title><div id=t>initial</div><script>
setTimeout(()=>{document.getElementById('t').textContent='updated';document.body.style.background='rgb(10,120,200)'},100);</script>`)
	case "/chrome-nav":
		servePage(w, `<title>nav</title><script>location.href='chrome://version/'</script>`)
	case "/srcdoc-frame":
		servePage(w, `<title>srcdoc</title><p id=p>drawn</p><script>var f=document.createElement('iframe');f.srcdoc='<p>x</p>';document.body.appendChild(f)</script>`)
	case "/file-nav":
		servePage(w, `<title>nav</title><script>location.href='file:///etc/hosts'</script>`)
	case "/scan-hostile":
		servePage(w, `<title>hostile</title><script>
window.__log=[];
function run(tag){window.__log.push([tag,
 String(window.open('`+f.B("/ran")+`')),
 String(alert('a')),String(confirm('c')),String(prompt('p'))])}
run('load');setTimeout(()=>run('timer'),150);
window.onbeforeunload=()=>'leave?';window.addEventListener('beforeunload',e=>{e.preventDefault();e.returnValue=''});
</script>`)
	case "/redirect":
		http.Redirect(w, r, "/gone", http.StatusFound)
	case "/gone":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><title>Gone</title><p>gone</p>"))
	case "/ran":
		servePage(w, `<title>ran</title><script>window.__ran=true;document.title='ran'</script><p>ran</p>`)
	case "/geo":
		servePage(w, `<title>geo</title>`)
	case "/dialog":
		servePage(w, `<title>dialog</title>`)
	case "/opener":
		servePage(w, `<title>opener</title><button id=b onclick="window.open('`+f.B("/ran")+`')">open</button>`)
	case "/chooser":
		servePage(w, `<title>chooser</title><input id=f type=file>`)
	case "/echo-headers":
		h := map[string]string{}
		for k := range r.Header {
			h[k] = r.Header.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h)
	case "/dl/offer":
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="offer-letter.pdf"`)
		_, _ = w.Write([]byte("%PDF-1.4 example"))
	case "/dl/hostile":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="../../.ssh/config"`)
		_, _ = w.Write([]byte("example"))
	case "/dl/slow":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="slow.bin"`)
		w.Header().Set("Content-Length", "100000")
		fl, _ := w.(http.Flusher)
		for range 100 {
			_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	case "/dl/link":
		servePage(w, `<title>dl</title><a id=a href="/dl/offer" download>get</a>`)
	default:
		http.NotFound(w, r)
	}
}

func servePage(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + body))
}

// onePixelPNG is a valid 1x1 image.
var onePixelPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89, 0, 0, 0, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0, 5, 0xfe, 2, 0xfe, 0xa7, 0x35, 0x81, 0x84, 0, 0, 0, 0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}

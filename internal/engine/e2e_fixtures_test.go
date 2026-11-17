//go:build browser

package engine

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
)

// fixtures serves synthetic application pages from two loopback sites whose
// registrable domains differ, so Chrome puts the second in its own process.
type fixtures struct {
	a, b    *httptest.Server
	c       *httptest.Server // HTTPS, for application-system hosts the browser pins to HTTPS
	mu      sync.Mutex
	hits    map[string]int
	uploads []upload
}

// upload is one file part of a multipart form the fixture server received.
type upload struct {
	Field, Name string
	Size        int
}

func newFixtures() *fixtures {
	f := &fixtures{hits: map[string]int{}}
	f.a = f.listen()
	f.b = f.listen()
	f.c = f.listenTLS()
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

func (f *fixtures) listenTLS() *httptest.Server {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	s.Listener = l
	s.StartTLS()
	return s
}

func (f *fixtures) close() { f.a.Close(); f.b.Close(); f.c.Close() }

func port(s *httptest.Server) int { return s.Listener.Addr().(*net.TCPAddr).Port }

func (f *fixtures) A(path string) string {
	return fmt.Sprintf("http://a.example.test:%d%s", port(f.a), path)
}

// R is a site the engine's frame registry admits without a probe (a
// SuccessFactors-family host), served by the first server.
func (f *fixtures) R(path string) string {
	return fmt.Sprintf("http://e2e.ns2cloud.com:%d%s", port(f.a), path)
}

func (f *fixtures) B(path string) string {
	return fmt.Sprintf("http://b.sitetwo.test:%d%s", port(f.b), path)
}

// hit counts the loads of a page per test case.
func (f *fixtures) hit(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[key]++
	return f.hits[key]
}

// reset clears what earlier tests left on the shared servers, so a test counts
// only its own loads and uploads (also under -count above 1).
func (f *fixtures) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = map[string]int{}
	f.uploads = nil
}

func (f *fixtures) hitCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func servePage(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + body))
}

// The top page. Query parameters shape one scenario:
//
//	case     names the scenario; loads are counted per case
//	reloads  the page reloads itself on the first input event of each of its
//	         first N loads, replacing the document mid-fill
//	block    milliseconds the page's main thread is blocked once, on the first input
//	frames   "none" leaves out the embedded forms
const topPage = `<title>Apply</title><h1>Careers</h1>
<form onsubmit="return false" id=f>
<label for=fn>First name</label><input id=fn name=fn>
<label for=ln>Last name</label><input id=ln name=ln>
<label for=em>Email</label><input id=em name=email type=email>
<label for=cv>Resume</label><input id=cv type=file accept=".pdf">
<fieldset><legend>Are you legally authorized to work in the United States?</legend>
<label><input type=radio name=wa1 value=Yes> Yes</label><label><input type=radio name=wa1 value=No> No</label></fieldset>
<label for=src>How did you hear about us?</label>
<select id=src><option value="">Select...</option><option>Job board</option><option>Referral</option><option>Careers site</option></select>
<fieldset><legend>Which languages do you use? Select all that apply</legend>
<label><input type=checkbox id=lg name="lang[]" value=go> Go</label><label><input type=checkbox id=lt name="lang[]" value=ts> TypeScript</label>
<label><input type=checkbox id=lr name="lang[]" value=rs> Rust</label></fieldset>
<label for=yrs>Years of experience with Go</label><input id=yrs>
</form>
%s
<script>
// A widget that wants a real file picker discards a file assigned by script.
const cv=document.getElementById('cv');
cv.addEventListener('change',e=>{ if(e.isTrusted){window.__trustedChange=true}else{e.target.value=''} });
const reloads=%d, block=%d;
document.addEventListener('input',()=>{
  if(window.__acted)return; window.__acted=true;
  if(block>0){const end=Date.now()+block; while(Date.now()<end){}}
  if(reloads>0){setTimeout(()=>location.reload(),0)}
},true);
</script>`

const sameFrame = `<title>Same origin</title>
<label for=city>City</label><input id=city name=city>
<label for=zip>Postal code</label><input id=zip name=zip>
<label for=ed1>Highest education level</label>
<select id=ed1><option value="">Select...</option><option>High school</option><option>Bachelor's degree</option><option>Master's degree</option></select>`

const crossFrame = `<title>Cross origin</title>
<label for=ph>Phone number</label><input id=ph name=phone type=tel>
<fieldset><legend>Do you require visa sponsorship?</legend>
<label><input type=radio name=sp1 value=Yes> Yes</label><label><input type=radio name=sp1 value=No> No</label></fieldset>
<label for=li>LinkedIn profile URL</label><input id=li name=linkedin>`

// A registry-admitted embed, and the third-party sign-in page it may be sent to.
const regFrame = `<title>Registry embed</title>
<label for=rn>First name</label><input id=rn name=fn>
<label for=re>Email</label><input id=re name=email type=email>
<label for=rs>Desired salary</label><input id=rs name=pay>`

const ssoPage = `<title>Sign in</title>
<label for=sn>First name</label><input id=sn name=fn>
<label for=se>Email</label><input id=se name=email type=email>
<label for=sp>Phone number</label><input id=sp name=phone type=tel>`

func (f *fixtures) receiveUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	for field, hs := range r.MultipartForm.File {
		for _, h := range hs {
			f.uploads = append(f.uploads, upload{Field: field, Name: h.Filename, Size: int(h.Size)})
		}
	}
	f.mu.Unlock()
	servePage(w, `<title>Received</title><p>ok</p>`)
}

func (f *fixtures) receivedUploads() []upload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upload(nil), f.uploads...)
}

func intParam(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func (f *fixtures) handle(w http.ResponseWriter, r *http.Request) {
	if body, ok := atsPage(r.URL.Path); ok {
		servePage(w, body)
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/apply":
		key := q.Get("case")
		n := f.hit(key)
		reloads := 0
		if n <= intParam(r, "reloads") {
			reloads = 1
		}
		frames := ""
		if q.Get("frames") == "reg" {
			frames = fmt.Sprintf(`<iframe id=reg src="%s" width=600 height=260></iframe>`, f.R("/embed-reg"))
		} else if q.Get("frames") != "none" {
			frames = fmt.Sprintf(`<iframe id=same src="/embed-same" width=600 height=260></iframe>
<iframe id=cross src="%s" width=600 height=260></iframe>
<iframe id=empty src="about:blank" width=10 height=10></iframe>`, f.B("/embed-cross"))
		}
		servePage(w, fmt.Sprintf(topPage, frames, reloads, intParam(r, "block")))
	case "/embed-same":
		servePage(w, sameFrame)
	case "/embed-cross":
		servePage(w, crossFrame)
	case "/embed-reg":
		servePage(w, regFrame)
	case "/sso":
		servePage(w, ssoPage)
	case "/upload":
		f.receiveUpload(w, r)
	case "/blank":
		servePage(w, `<title>blank</title><p>nothing to fill</p>`)
	default:
		http.NotFound(w, r)
	}
}

package browser

import (
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func (w *ResponseWatch) fakeRequest(id string, method, typ string) {
	w.onRequest(network.EventRequestWillBeSent{RequestID: network.RequestID(id), Type: network.ResourceType(typ), Request: &network.Request{Method: method, URL: "https://jobs.example.test/api/x?token=secret"}})
}

func (w *ResponseWatch) fakeAnswer(id string, status int) {
	w.onResponse(network.EventResponseReceived{RequestID: network.RequestID(id), Type: "Fetch", Response: &network.Response{URL: "https://jobs.example.test/api/x", Status: int64(status)}})
}

// Chromedp hands each network event type to its own subscription, so the
// events of one request are handled in any order. Every order gives the same
// one record, and nothing stays pending.
func TestBrowser_Observation_RecordsARequestWhateverOrderItsEventsAreHandled(t *testing.T) {
	steps := map[string]func(w *ResponseWatch){
		"request":  func(w *ResponseWatch) { w.fakeRequest("r", "POST", "Fetch") },
		"response": func(w *ResponseWatch) { w.fakeAnswer("r", 201) },
		"finished": func(w *ResponseWatch) { w.onFinished(network.EventLoadingFinished{RequestID: "r"}) },
	}
	for _, order := range [][]string{
		{"request", "response", "finished"}, {"request", "finished", "response"}, {"response", "request", "finished"},
		{"response", "finished", "request"}, {"finished", "request", "response"}, {"finished", "response", "request"},
	} {
		w := newResponseWatch()
		for _, s := range order {
			steps[s](w)
		}
		got := w.Responses()
		if len(got) != 1 || got[0].Method != "POST" || got[0].Status != 201 || w.Pending() != 0 {
			t.Errorf("%v: %+v, %d pending", order, got, w.Pending())
		}
	}
	// A failure that is handled before its request leaves nothing pending.
	w := newResponseWatch()
	w.onFailed(network.EventLoadingFailed{RequestID: "f"})
	w.fakeRequest("f", "POST", "Fetch")
	if w.Pending() != 0 || len(w.Responses()) != 0 {
		t.Fatalf("a failed request: %d pending, %+v", w.Pending(), w.Responses())
	}
}

// A classic form submit answers its POST with a redirect to a thank-you page.
// The browser reports the hop as a second request event of the same request,
// carrying the answer to the first: the write keeps its own record, with the
// status the service gave it, and the page it led to is recorded apart.
func TestBrowser_Observation_ARedirectedWriteKeepsItsOwnRecord(t *testing.T) {
	hop := func(w *ResponseWatch) {
		w.onRequest(network.EventRequestWillBeSent{RequestID: "r", Type: "Document",
			Request:          &network.Request{Method: "GET", URL: "https://jobs.example.test/thanks?id=7"},
			RedirectResponse: &network.Response{URL: "https://jobs.example.test/apply?token=secret", Status: 302}})
	}
	post := func(w *ResponseWatch) {
		w.onRequest(network.EventRequestWillBeSent{RequestID: "r", Type: "Document", Request: &network.Request{Method: "POST", URL: "https://jobs.example.test/apply?token=secret"}})
	}
	landed := func(w *ResponseWatch) {
		w.onResponse(network.EventResponseReceived{RequestID: "r", Type: "Document", Response: &network.Response{URL: "https://jobs.example.test/thanks?id=7", Status: 200}})
		w.onFinished(network.EventLoadingFinished{RequestID: "r"})
	}
	// The answer of the last hop is handled before or after the hop itself.
	for name, steps := range map[string][]func(*ResponseWatch){"in order": {post, hop, landed}, "answer first": {post, landed, hop}} {
		w := newResponseWatch()
		for _, step := range steps {
			step(w)
		}
		got := w.Responses()
		if len(got) != 2 || w.Pending() != 0 {
			t.Fatalf("%s: %+v, %d pending", name, got, w.Pending())
		}
		if got[0].Method != "POST" || got[0].Status != 302 || got[0].URL != "https://jobs.example.test/apply" || !WriteFilter.matches(got[0]) {
			t.Errorf("%s: the write: %+v", name, got[0])
		}
		if got[1].Method != "GET" || got[1].Status != 200 || got[1].URL != "https://jobs.example.test/thanks" {
			t.Errorf("%s: the page it led to: %+v", name, got[1])
		}
	}
	// A hop of a request that began before the watch has no known method: no
	// write is made up for it.
	w := newResponseWatch()
	hop(w)
	landed(w)
	if got := w.Responses(); len(got) != 0 {
		t.Fatalf("a request that began before the watch: %+v", got)
	}
}

func TestBrowser_Observation_KeepsTheNewestTwoHundredAndFiltersByMethodTypeAndAddress(t *testing.T) {
	w := newResponseWatch()
	for n := range 250 {
		id, address := network.RequestID(fmt.Sprint(n)), fmt.Sprintf("https://jobs.example.test/api/x/%d?token=secret", n)
		w.onRequest(network.EventRequestWillBeSent{RequestID: id, Type: "XHR", Request: &network.Request{Method: "get", URL: address}})
		w.onResponse(network.EventResponseReceived{RequestID: id, Type: "XHR", Response: &network.Response{URL: address, Status: 200}})
		w.onFinished(network.EventLoadingFinished{RequestID: id})
	}
	got := w.Responses()
	if len(got) != maxWatchedResponses {
		t.Fatalf("%d records", len(got))
	}
	// The oldest fifty are the ones dropped.
	if got[0].URL != "https://jobs.example.test/api/x/50" || got[len(got)-1].URL != "https://jobs.example.test/api/x/249" {
		t.Fatalf("kept %s to %s, want requests 50 to 249", got[0].URL, got[len(got)-1].URL)
	}
	if got[0].At.IsZero() || got[0].Type != "XHR" || got[0].Status != 200 {
		t.Fatalf("a record carries url (reduced), method, status, type and time: %+v", got[0])
	}
	r := Response{Method: "POST", URL: "https://jobs.example.test/api/apply", Type: "Fetch"}
	for name, f := range map[string]ResponseFilter{
		"substring": {URL: "/api/apply"}, "method, any case": {Method: "post"}, "type": {Type: "fetch"}, "all three": {URL: "apply", Method: "POST", Type: "Fetch"}, "writes": WriteFilter,
	} {
		if !f.matches(r) {
			t.Errorf("%s does not match", name)
		}
	}
	for name, f := range map[string]ResponseFilter{"url": {URL: "/api/other"}, "method": {Method: "GET"}, "type": {Type: "XHR"}} {
		if f.matches(r) {
			t.Errorf("%s matches", name)
		}
	}
}

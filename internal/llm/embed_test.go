package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

type embedSrv struct {
	*oaServer
	mu     sync.Mutex
	inputs [][]string
}

func vecFor(s string, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(len(s)) + float32(i)/10 + float32(s[len(s)-1])
	}
	return v
}

func newEmbedServer(t *testing.T, reorder bool, shortBy int) *embedSrv {
	t.Helper()
	es := &embedSrv{}
	es.oaServer = newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		var body struct {
			Model string
			Input []string
		}
		_ = decodeBody(r, &body)
		es.mu.Lock()
		es.inputs = append(es.inputs, body.Input)
		es.mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i, in := range body.Input[:len(body.Input)-shortBy] {
			data = append(data, item{i, vecFor(in+body.Model, 4)})
		}
		if reorder {
			for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
				data[i], data[j] = data[j], data[i]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]any{"prompt_tokens": 7}})
	})
	return es
}

func embedEnv(t *testing.T, es *embedSrv, model string) *env {
	t.Helper()
	e := newEnv(t)
	e.provider(oaProvider("a", es.oaServer), fakeKey("em"))
	e.route("embedding", Route{Provider: "a", Model: model})
	return e
}

func TestLLM_Embed_BatchLimit(t *testing.T) {
	es := newEmbedServer(t, false, 0)
	e := embedEnv(t, es, "emb-1")
	texts := make([]string, 100)
	for i := range texts {
		texts[i] = fmt.Sprintf("text %d", i)
	}
	out, err := e.r.Embed(bg(), texts, EmbedOptions{})
	if err != nil || len(out.Vectors) != 100 {
		t.Fatalf("err %v vectors %d", err, len(out.Vectors))
	}
	if len(es.inputs) != 2 || len(es.inputs[0]) != 64 || len(es.inputs[1]) != 36 {
		t.Fatalf("batches = %d", len(es.inputs))
	}
	if out.Model != "emb-1" {
		t.Fatalf("model = %s", out.Model)
	}
}

func TestLLM_Embed_ReplyOrderedByIndexMissingIsError(t *testing.T) {
	t.Run("reply out of order", func(t *testing.T) {
		es := newEmbedServer(t, true, 0)
		e := embedEnv(t, es, "emb-1")
		texts := []string{"a", "bbb", "cc"}
		out, err := e.r.Embed(bg(), texts, EmbedOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for i, tx := range texts {
			if got := out.Vectors[i][0]; got != vecFor(tx+"emb-1", 4)[0] {
				t.Errorf("vector %d belongs to another input: %v", i, got)
			}
		}
	})
	t.Run("missing vector", func(t *testing.T) {
		es := newEmbedServer(t, false, 1)
		e := embedEnv(t, es, "emb-1")
		texts := make([]string, 64)
		for i := range texts {
			texts[i] = fmt.Sprint("t", i)
		}
		_, err := e.r.Embed(bg(), texts, EmbedOptions{})
		wantKind(t, err, KindEmptyCompletion)
	})
}

func TestLLM_Embed_CacheKeyIncludesModel(t *testing.T) {
	es := newEmbedServer(t, false, 0)
	e := embedEnv(t, es, "emb-1")
	first, _ := e.r.Embed(bg(), []string{"hello"}, EmbedOptions{})
	e.r.Embed(bg(), []string{"hello"}, EmbedOptions{})
	if len(es.inputs) != 1 {
		t.Fatalf("a repeat was not served from the cache: %d requests", len(es.inputs))
	}
	e.route("embedding", Route{Provider: "a", Model: "emb-2"})
	second, err := e.r.Embed(bg(), []string{"hello"}, EmbedOptions{})
	if err != nil || len(es.inputs) != 2 {
		t.Fatalf("model change: err %v requests %d", err, len(es.inputs))
	}
	if second.Vectors[0][0] == first.Vectors[0][0] || second.Model != "emb-2" {
		t.Fatal("a vector of the old model was returned for the new one")
	}
}

func TestLLM_Embed_FallbackModelNeverMixesVectors(t *testing.T) {
	bad := newOAServer(t, statusHandler(503, `{}`))
	good := newEmbedServer(t, false, 0)
	e := newEnv(t)
	e.provider(oaProvider("a", bad), fakeKey("e1"))
	e.provider(oaProvider("b", good.oaServer), fakeKey("e2"))
	e.route("embedding", Route{Provider: "a", Model: "emb-1", Fallbacks: []Fallback{{"b", "emb-2"}}})
	out, err := e.r.Embed(bg(), []string{"x"}, EmbedOptions{})
	if err != nil || out.Model != "emb-2" {
		t.Fatalf("out %+v err %v", out, err)
	}
}

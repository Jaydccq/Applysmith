package llm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	embedBatch    = 64
	embedMaxChars = 8000
	embedCacheMax = 50000
)

// EmbedOptions carries the optional parts of an embedding call.
type EmbedOptions struct {
	RunID    string
	Deadline time.Time
}

// Embeddings is the result of Embed: one vector per input text, all from one
// model.
type Embeddings struct {
	Vectors [][]float32
	Model   string
}

// Embed embeds the texts, at most 64 per request and each cut to 8,000
// characters, serving repeats from the SQLite cache keyed by model and text
// hash. When the embedding task is unrouted it returns ErrNoRoute so the caller
// can fall back to lexical search.
func (r *Router) Embed(ctx context.Context, texts []string, opts EmbedOptions) (*Embeddings, error) {
	cut := make([]string, len(texts))
	for i, t := range texts {
		if rs := []rune(t); len(rs) > embedMaxChars {
			t = string(rs[:embedMaxChars])
		}
		cut[i] = t
	}
	return r.embedPass(ctx, cut, opts, nil)
}

func (r *Router) embedPass(ctx context.Context, texts []string, opts EmbedOptions, pin *Fallback) (*Embeddings, error) {
	base := Request{Task: "embedding", RunID: opts.RunID, Deadline: opts.Deadline, pin: pin}
	cs, err := r.prepare(ctx, base)
	if err != nil {
		return nil, err
	}
	model := cs.cands[0].model
	vecs := make([][]float32, len(texts))
	hashes := make([]string, len(texts))
	for i, t := range texts {
		hashes[i] = textHash(t)
	}
	r.cacheGet(ctx, model, hashes, vecs)
	var missing []int
	for i, v := range vecs {
		if v == nil {
			missing = append(missing, i)
		}
	}
	for start := 0; start < len(missing); start += embedBatch {
		idx := missing[start:min(start+embedBatch, len(missing))]
		batch := make([]string, len(idx))
		for i, j := range idx {
			batch[i] = texts[j]
		}
		bcs := cs
		if start > 0 {
			if bcs, err = r.prepare(ctx, base); err != nil {
				return nil, err
			}
		}
		out, err := r.embedBatch(ctx, bcs, batch)
		if err != nil {
			return nil, err
		}
		served := bcs.won.cand
		if served.model != model {
			if pin != nil {
				return nil, &Error{Kind: KindOther, Task: "embedding", Message: "the pinned embedding model changed during the call"}
			}
			// A fallback served the batch: vectors of two models are never mixed,
			// so the whole call restarts on that model.
			return r.embedPass(ctx, texts, opts, &Fallback{served.provider.ID, served.model})
		}
		for i, j := range idx {
			vecs[j] = out[i]
		}
		r.cachePut(ctx, model, idx, hashes, vecs)
	}
	return &Embeddings{Vectors: vecs, Model: model}, nil
}

func (r *Router) embedBatch(ctx context.Context, cs *callState, batch []string) ([][]float32, error) {
	var out [][]float32
	err := r.walk(ctx, cs, func(actx context.Context, a *attempt) error {
		var vecs [][]float32
		var err error
		switch a.cand.provider.Kind {
		case KindOpenAI:
			vecs, err = r.openAIEmbed(actx, a, batch)
		case KindFake:
			vecs = make([][]float32, len(batch))
			for i, t := range batch {
				vecs[i] = fakeVector(t)
			}
			a.usage.Input = fakeTokens(strings.Join(batch, ""))
		default:
			err = &Error{Kind: KindBadRequest, Message: "the " + a.cand.provider.Kind + " kind has no embeddings"}
		}
		if err != nil {
			return err
		}
		out = vecs
		return nil
	})
	return out, err
}

// openAIEmbed calls the embeddings endpoint and places each vector by the
// reply's index. A missing or short vector fails the attempt.
func (r *Router) openAIEmbed(ctx context.Context, a *attempt, batch []string) ([][]float32, error) {
	p := a.cand.provider
	resp, err := r.post(ctx, joinURL(p.BaseURL, "/embeddings"), map[string]string{"Authorization": "Bearer " + a.key},
		map[string]any{"model": a.cand.model, "input": batch}, a.key)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode
	var body struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, &Error{Kind: KindOther, Message: "unreadable embeddings reply"}
	}
	a.usage.Input = body.Usage.PromptTokens
	vecs := make([][]float32, len(batch))
	for _, d := range body.Data {
		if d.Index >= 0 && d.Index < len(vecs) && len(d.Embedding) > 0 {
			vecs[d.Index] = d.Embedding
		}
	}
	for _, v := range vecs {
		if v == nil {
			return nil, &Error{Kind: KindEmptyCompletion, Message: "the embeddings reply lacks a vector for an input"}
		}
		if len(v) != len(vecs[0]) {
			return nil, &Error{Kind: KindEmptyCompletion, Message: "the embeddings reply holds vectors of different dimensions"}
		}
	}
	return vecs, nil
}

func textHash(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// cacheGet fills vecs for the hashes found under the model; failures are
// logged and read as misses.
func (r *Router) cacheGet(ctx context.Context, model string, hashes []string, vecs [][]float32) {
	now := timeText(r.now())
	err := r.store.Write(ctx, func(tx *sql.Tx) error {
		for i, h := range hashes {
			var blob []byte
			err := tx.QueryRowContext(ctx, `SELECT vector FROM llm_embeddings WHERE model = ? AND text_hash = ?`, model, h).Scan(&blob)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			vecs[i] = decodeVec(blob)
			if _, err := tx.ExecContext(ctx, `UPDATE llm_embeddings SET used_at = ? WHERE model = ? AND text_hash = ?`, now, model, h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.log.Error("llm: embedding cache read failed", "err", err)
		for i := range vecs {
			vecs[i] = nil
		}
	}
}

// cachePut stores new vectors and evicts the least recently used beyond the
// limit.
func (r *Router) cachePut(ctx context.Context, model string, idx []int, hashes []string, vecs [][]float32) {
	now := timeText(r.now())
	err := r.store.Write(ctx, func(tx *sql.Tx) error {
		for _, j := range idx {
			if _, err := tx.ExecContext(ctx, `INSERT INTO llm_embeddings (model, text_hash, vector, used_at) VALUES (?,?,?,?)
				ON CONFLICT(model, text_hash) DO UPDATE SET vector = excluded.vector, used_at = excluded.used_at`,
				model, hashes[j], encodeVec(vecs[j]), now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM llm_embeddings WHERE rowid IN
			(SELECT rowid FROM llm_embeddings ORDER BY used_at ASC, rowid ASC LIMIT max(0, (SELECT count(*) FROM llm_embeddings) - ?))`, embedCacheMax)
		return err
	})
	if err != nil {
		r.log.Error("llm: embedding cache write failed", "err", err)
	}
}

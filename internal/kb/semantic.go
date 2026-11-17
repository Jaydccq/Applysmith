package kb

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// Semantic stage thresholds.
const (
	semanticFloor    = 0.20
	semanticStandout = 0.06
)

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

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type semCandidate struct {
	doc *indexedDoc
	sec *indexedSection
}

// semantic ranks the sections of published snippets by cosine similarity to the
// query. It sends nothing but sections of published snippets to the embedding
// provider, never repeats a snippet the lexical stage returned, and refuses a
// pack whose best candidate does not stand out. A provider failure returns no
// hits and a diagnostic, so the caller keeps the lexical result.
func (s *Service) semantic(ctx context.Context, q Query, docs []*indexedDoc, lexical []hit, limit int) ([]hit, []string) {
	skip := map[string]bool{}
	for _, h := range lexical {
		if h.doc.Kind == KindSnippet {
			skip[h.doc.ID] = true
		}
	}
	filter := newKindsFilter(q.Kinds)
	var cands []semCandidate
	for _, d := range docs {
		if d.Kind != KindSnippet || skip[d.ID] || !filter.allows(d) {
			continue
		}
		for i := range d.secs {
			cands = append(cands, semCandidate{doc: d, sec: &d.secs[i]})
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	qv, model, err := s.opts.Embedder.Embed(ctx, []string{q.Query})
	if err != nil || len(qv) != 1 {
		return nil, []string{"semantic retrieval skipped: " + errText(err, "the embedding provider returned no query vector")}
	}
	vecs, err := s.sectionVectors(ctx, model, cands)
	if err != nil {
		return nil, []string{"semantic retrieval skipped: " + err.Error()}
	}
	best := map[string]*hit{}
	for i, c := range cands {
		if vecs[i] == nil {
			continue
		}
		sim := cosine(qv[0], vecs[i])
		if sim < semanticFloor {
			continue
		}
		if cur := best[c.doc.ID]; cur == nil || sim > cur.score {
			best[c.doc.ID] = &hit{doc: c.doc, sec: c.sec, score: sim, reasons: []string{"semantic"}, stage: StageSemantic}
		}
	}
	var hits []hit
	for _, h := range best {
		hits = append(hits, *h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].doc.ID < hits[j].doc.ID
	})
	if len(hits) >= 2 && hits[0].score-hits[1].score < semanticStandout {
		return nil, []string{"semantic retrieval refused: the best candidate does not lead the runner-up"}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func errText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// sectionVectors returns one vector per candidate, from the cache keyed by the
// section's content hash and the model, embedding and storing only the missing
// ones. Vectors of two models are never mixed.
func (s *Service) sectionVectors(ctx context.Context, model string, cands []semCandidate) ([][]float32, error) {
	out := make([][]float32, len(cands))
	need := make(map[string]bool, len(cands))
	for _, c := range cands {
		need[c.sec.Hash] = true
	}
	cached := map[string][]float32{}
	rows, err := s.st.Read().QueryContext(ctx, `SELECT section_hash, vector FROM kb_embeddings WHERE model = ?`, model)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h string
		var b sql.RawBytes
		if err := rows.Scan(&h, &b); err != nil {
			rows.Close()
			return nil, err
		}
		// Only a candidate's vector is decoded; the rest are passed over.
		if need[h] {
			cached[h] = decodeVec(b)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var texts []string
	var hashes []string
	queued := map[string]bool{}
	for _, c := range cands {
		if cached[c.sec.Hash] == nil && !queued[c.sec.Hash] {
			queued[c.sec.Hash] = true
			hashes = append(hashes, c.sec.Hash)
			texts = append(texts, c.sec.Heading+"\n"+c.sec.Text)
		}
	}
	if len(texts) > 0 {
		vecs, served, err := s.opts.Embedder.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		if served != model || len(vecs) != len(texts) {
			return nil, fmt.Errorf("the embedding model changed during the call")
		}
		now := s.stamp()
		err = s.st.Write(ctx, func(tx *sql.Tx) error {
			for i, h := range hashes {
				if _, err := tx.ExecContext(ctx, `INSERT INTO kb_embeddings (model, section_hash, vector, used_at) VALUES (?, ?, ?, ?)
					ON CONFLICT (model, section_hash) DO UPDATE SET used_at = excluded.used_at`, model, h, encodeVec(vecs[i]), now); err != nil {
					return err
				}
				cached[h] = vecs[i]
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for i, c := range cands {
		out[i] = cached[c.sec.Hash]
	}
	return out, nil
}

package toolsearch

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"

	"github.com/cloudwego/eino/components/embedding"
)

type Vector struct {
	Embedder embedding.Embedder
	// File mirrors the index to disk, written on every Replace; empty means
	// memory only.
	File string

	mu    sync.Mutex
	store map[string]map[string]indexed // key -> tool name -> entry+vector
}

type indexed struct {
	Entry
	Vector []float64 `json:"vector"`
}

// NewVector builds the index, loading the mirror file when it is readable
// (an unreadable or corrupt file is discarded, not fatal: the index is
// derived state, rebuilt on the next Replace).
func NewVector(e embedding.Embedder, file string) *Vector {
	v := &Vector{Embedder: e, File: file, store: map[string]map[string]indexed{}}
	if file != "" {
		if data, err := os.ReadFile(file); err == nil {
			_ = json.Unmarshal(data, &v.store)
		}
	}
	return v
}

// Replace implements Index.
func (v *Vector) Replace(ctx context.Context, key string, entries []Entry) error {
	if len(entries) == 0 {
		v.mu.Lock()
		delete(v.store, key)
		v.mu.Unlock()
		return v.persist()
	}
	texts := make([]string, len(entries))
	for i, e := range entries {
		texts[i] = e.Name + "\n" + e.Desc
	}
	vectors, err := v.Embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return err
	}
	m := make(map[string]indexed, len(entries))
	for i, e := range entries {
		if i < len(vectors) {
			m[e.Name] = indexed{Entry: e, Vector: vectors[i]}
		}
	}
	v.mu.Lock()
	v.store[key] = m
	v.mu.Unlock()
	return v.persist()
}

// Search implements Index.
func (v *Vector) Search(ctx context.Context, key, query string, n int) ([]Entry, error) {
	if n <= 0 {
		n = 5
	}
	embedded, err := v.Embedder.EmbedStrings(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(embedded) == 0 {
		return nil, nil
	}
	queryVec := embedded[0]
	v.mu.Lock()
	defer v.mu.Unlock()
	type scored struct {
		entry Entry
		score float64
	}
	var out []scored
	for _, idx := range v.store[key] {
		out = append(out, scored{entry: idx.Entry, score: cosine(queryVec, idx.Vector)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > n {
		out = out[:n]
	}
	entries := make([]Entry, 0, len(out))
	for _, s := range out {
		entries = append(entries, s.entry)
	}
	return entries, nil
}

func (v *Vector) persist() error {
	if v.File == "" {
		return nil
	}
	v.mu.Lock()
	data, err := json.Marshal(v.store)
	v.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(v.File, data, 0o644)
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

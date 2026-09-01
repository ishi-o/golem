package agent_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/knowledge"
)

// testKnowledgeBase is deliberately a test double, not a library backend.
// Production code is expected to implement knowledge.KnowledgeBase around an
// Eino/eino-ext indexer and retriever chosen by the application.
type testKnowledgeBase struct {
	mu   sync.RWMutex
	docs map[string]testKnowledgeDocument
}

type testKnowledgeDocument struct {
	owned     knowledge.Scope
	content   string
	metadata  map[string]any
	createdAt time.Time
}

func newTestKnowledgeBase() *testKnowledgeBase {
	return &testKnowledgeBase{docs: map[string]testKnowledgeDocument{}}
}

func (b *testKnowledgeBase) Index(ctx context.Context, source knowledge.Source) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := source.Validate(); err != nil {
		return "", err
	}
	text := source.Text
	if strings.TrimSpace(text) == "" {
		data, err := os.ReadFile(source.Location)
		if err != nil {
			return "", fmt.Errorf("test knowledge: read %q: %w", source.Location, err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("test knowledge: document %q is empty", source.DocID)
	}
	createdAt := source.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	owned := source.Scope.Owning(source.Target)
	document := testKnowledgeDocument{
		owned:     owned,
		content:   text,
		createdAt: createdAt,
		metadata:  knowledge.MetadataFor(owned, source.DocID, source.Title, source.Location, createdAt, 0, 1),
	}
	b.mu.Lock()
	b.docs[documentKey(owned, source.DocID)] = document
	b.mu.Unlock()
	return source.DocID, nil
}

func (b *testKnowledgeBase) List(ctx context.Context, scope knowledge.Scope, offset, limit int) (knowledge.Page, error) {
	if err := ctx.Err(); err != nil {
		return knowledge.Page{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	readable := knowledge.ReadableBy(scope)
	b.mu.RLock()
	entries := make([]knowledge.Entry, 0, len(b.docs))
	for _, document := range b.docs {
		metadata := knowledge.ReadMetadata(document.metadata)
		if !readable(metadata) {
			continue
		}
		_, target := knowledge.ScopeForMetadata(document.metadata)
		entries = append(entries, knowledge.Entry{
			DocID: metadata.DocID, Title: metadata.Title, Location: metadata.Source,
			ChunkCount: metadata.ChunkCount, CreatedAt: metadata.CreatedAt, Target: target,
		})
	}
	b.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].DocID < entries[j].DocID })
	if offset >= len(entries) {
		return knowledge.Page{Entries: []knowledge.Entry{}}, nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	return knowledge.Page{Entries: entries[offset:end], HasMore: end < len(entries)}, nil
}

func (b *testKnowledgeBase) Delete(ctx context.Context, scope knowledge.Scope, docID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	readable := knowledge.DocumentReadableBy(scope, strings.TrimSpace(docID))
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, document := range b.docs {
		if readable(knowledge.ReadMetadata(document.metadata)) {
			delete(b.docs, key)
		}
	}
	return nil
}

func (b *testKnowledgeBase) Move(ctx context.Context, scope knowledge.Scope, docID string, target knowledge.Target) (knowledge.Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return knowledge.Entry{}, false, err
	}
	if !scope.Reachable(target) {
		return knowledge.Entry{}, false, fmt.Errorf("test knowledge: target %q is not reachable", target)
	}
	docID = strings.TrimSpace(docID)
	readable := knowledge.DocumentReadableBy(scope, docID)
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, document := range b.docs {
		metadata := knowledge.ReadMetadata(document.metadata)
		if !readable(metadata) {
			continue
		}
		owned := scope.Owning(target)
		document.owned = owned
		document.metadata = knowledge.MetadataFor(owned, metadata.DocID, metadata.Title, metadata.Source, document.createdAt, metadata.Chunk, metadata.ChunkCount)
		delete(b.docs, key)
		b.docs[documentKey(owned, docID)] = document
		return knowledge.Entry{DocID: metadata.DocID, Title: metadata.Title, Location: metadata.Source, ChunkCount: metadata.ChunkCount, CreatedAt: metadata.CreatedAt, Target: normalizedTarget(target)}, true, nil
	}
	return knowledge.Entry{}, false, nil
}

func (b *testKnowledgeBase) Search(ctx context.Context, scope knowledge.Scope, query string, topK int) ([]*schema.Document, error) {
	return b.search(ctx, scope, query, topK, nil)
}

func (b *testKnowledgeBase) SearchFiltered(ctx context.Context, scope knowledge.Scope, query string, topK int, filter func(knowledge.Metadata) bool) ([]*schema.Document, error) {
	return b.search(ctx, scope, query, topK, filter)
}

func (b *testKnowledgeBase) search(ctx context.Context, scope knowledge.Scope, query string, topK int, filter func(knowledge.Metadata) bool) ([]*schema.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("test knowledge: query is required")
	}
	if topK <= 0 {
		topK = 4
	}
	readable := knowledge.ReadableBy(scope)
	type scored struct {
		document *schema.Document
		score    float64
	}
	b.mu.RLock()
	results := make([]scored, 0, len(b.docs))
	for _, document := range b.docs {
		metadata := knowledge.ReadMetadata(document.metadata)
		if !readable(metadata) || (filter != nil && !filter(metadata)) {
			continue
		}
		content := strings.ToLower(document.content)
		score := testTextScore(query, content)
		if score == 0 {
			continue
		}
		metadataCopy := make(map[string]any, len(document.metadata))
		for key, value := range document.metadata {
			metadataCopy[key] = value
		}
		result := &schema.Document{ID: metadata.DocID, Content: document.content, MetaData: metadataCopy}
		result.WithScore(score)
		results = append(results, scored{document: result, score: score})
	}
	b.mu.RUnlock()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].document.ID < results[j].document.ID
		}
		return results[i].score > results[j].score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	documents := make([]*schema.Document, 0, len(results))
	for _, result := range results {
		documents = append(documents, result.document)
	}
	return documents, nil
}

func documentKey(scope knowledge.Scope, docID string) string {
	return scope.Owner + "\x00" + scope.Group + "\x00" + scope.Tenant + "\x00" + docID
}

func normalizedTarget(target knowledge.Target) knowledge.Target {
	if target == "" {
		return knowledge.TargetOwn
	}
	return target
}

func testTextScore(query, content string) float64 {
	if strings.Contains(content, query) {
		return 1
	}
	matched := 0
	words := strings.Fields(query)
	for _, word := range words {
		if strings.Contains(content, word) {
			matched++
		}
	}
	if len(words) == 0 {
		return 0
	}
	return float64(matched) / float64(len(words))
}

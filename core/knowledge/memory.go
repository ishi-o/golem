package knowledge

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/schema"
)

// InMemoryOptions configures the reference backend. It is intentionally
// small: production deployments should use an Eino indexer/retriever backed by
// their chosen vector store, while tests and single-process applications can
// use this implementation without another database.
type InMemoryOptions struct {
	Embedder     embedding.Embedder
	ChunkSize    int
	ChunkOverlap int
	Clock        func() time.Time
}

type memoryChunk struct {
	docID    string
	scopeKey string
	content  string
	metadata map[string]any
	vector   []float64
}

// InMemoryBase is a concurrency-safe, Eino-compatible reference knowledge
// base. It uses embeddings when supplied and deterministic token overlap when
// not, which keeps the facade testable without a remote model.
type InMemoryBase struct {
	embedder  embedding.Embedder
	chunkSize int
	overlap   int
	clock     func() time.Time

	mu     sync.RWMutex
	chunks []memoryChunk
}

// NewInMemory constructs the reference implementation.
func NewInMemory(options InMemoryOptions) *InMemoryBase {
	if options.ChunkSize <= 0 {
		options.ChunkSize = 1200
	}
	if options.ChunkOverlap < 0 {
		options.ChunkOverlap = 0
	}
	if options.ChunkOverlap >= options.ChunkSize {
		options.ChunkOverlap = options.ChunkSize / 5
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &InMemoryBase{embedder: options.Embedder, chunkSize: options.ChunkSize, overlap: options.ChunkOverlap, clock: options.Clock}
}

// Index implements KnowledgeBase.
func (b *InMemoryBase) Index(ctx context.Context, source Source) (string, error) {
	if b == nil {
		return "", fmt.Errorf("knowledge: nil in-memory base")
	}
	if err := source.Validate(); err != nil {
		return "", err
	}
	text := source.Text
	if strings.TrimSpace(text) == "" {
		data, err := os.ReadFile(source.Location)
		if err != nil {
			return "", fmt.Errorf("knowledge: read %q: %w", source.Location, err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("knowledge: document %q is empty", source.DocID)
	}
	parts := splitText(text, b.chunkSize, b.overlap)
	if len(parts) == 0 {
		return "", fmt.Errorf("knowledge: document %q is empty", source.DocID)
	}
	created := source.CreatedAt
	if created.IsZero() {
		created = b.clock()
	}
	owned := source.Scope.Owning(source.Target)
	vectors, err := b.embed(ctx, parts)
	if err != nil {
		return "", err
	}
	chunks := make([]memoryChunk, len(parts))
	key := scopeKey(owned)
	for i, part := range parts {
		chunks[i] = memoryChunk{
			docID: source.DocID, scopeKey: key, content: part,
			metadata: MetadataFor(owned, source.DocID, source.Title, source.Location, created, i, len(parts)),
		}
		if vectors != nil {
			chunks[i].vector = vectors[i]
		}
	}
	b.mu.Lock()
	filtered := b.chunks[:0]
	for _, chunk := range b.chunks {
		if chunk.docID != source.DocID || chunk.scopeKey != key {
			filtered = append(filtered, chunk)
		}
	}
	b.chunks = append(filtered, chunks...)
	b.mu.Unlock()
	return source.DocID, nil
}

func (b *InMemoryBase) embed(ctx context.Context, parts []string) ([][]float64, error) {
	if b.embedder == nil {
		return nil, nil
	}
	vectors, err := b.embedder.EmbedStrings(ctx, parts)
	if err != nil {
		return nil, fmt.Errorf("knowledge: embed document: %w", err)
	}
	if len(vectors) != len(parts) {
		return nil, fmt.Errorf("knowledge: embedder returned %d vectors for %d chunks", len(vectors), len(parts))
	}
	return vectors, nil
}

// List implements KnowledgeBase.
func (b *InMemoryBase) List(ctx context.Context, scope Scope, offset, limit int) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	readable := ReadableBy(scope)
	b.mu.RLock()
	byID := map[string]Entry{}
	for _, chunk := range b.chunks {
		metadata := ReadMetadata(chunk.metadata)
		if !FirstChunk(metadata) || !readable(metadata) {
			continue
		}
		_, target := ScopeForMetadata(chunk.metadata)
		byID[metadata.DocID] = Entry{DocID: metadata.DocID, Title: metadata.Title, Location: metadata.Source, ChunkCount: metadata.ChunkCount, CreatedAt: metadata.CreatedAt, Target: target}
	}
	b.mu.RUnlock()
	entries := make([]Entry, 0, len(byID))
	for _, entry := range byID {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].DocID < entries[j].DocID
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if offset >= len(entries) {
		return Page{Entries: []Entry{}}, nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	page := Page{Entries: append([]Entry(nil), entries[offset:end]...)}
	page.HasMore = end < len(entries)
	return page, nil
}

// Delete implements KnowledgeBase. It follows the facade's read scope: a
// caller may remove a document it can reach, while an inaccessible id is a
// deliberate no-op so deletion cannot become an oracle for another scope.
func (b *InMemoryBase) Delete(ctx context.Context, scope Scope, docID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	readable := DocumentReadableBy(scope, strings.TrimSpace(docID))
	docID = strings.TrimSpace(docID)
	b.mu.Lock()
	filtered := b.chunks[:0]
	for _, chunk := range b.chunks {
		metadata := ReadMetadata(chunk.metadata)
		if readable(metadata) {
			continue
		}
		filtered = append(filtered, chunk)
	}
	b.chunks = filtered
	b.mu.Unlock()
	return nil
}

// Move implements KnowledgeBase. It moves exactly the document that the
// caller can read, retaining its id, content, title and source. The caller is
// responsible for deciding whether a reachable document may be moved.
func (b *InMemoryBase) Move(ctx context.Context, scope Scope, docID string, target Target) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	if !scope.Reachable(target) {
		return Entry{}, false, fmt.Errorf("knowledge: target %q is not reachable from the request scope", target)
	}
	readable := DocumentReadableBy(scope, strings.TrimSpace(docID))
	docID = strings.TrimSpace(docID)
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := map[string]struct{}{}
	var entry Entry
	found := false
	for _, chunk := range b.chunks {
		metadata := ReadMetadata(chunk.metadata)
		if !readable(metadata) {
			continue
		}
		keys[chunk.scopeKey] = struct{}{}
		if !found {
			_, currentTarget := ScopeForMetadata(chunk.metadata)
			entry = Entry{DocID: metadata.DocID, Title: metadata.Title, Location: metadata.Source, ChunkCount: metadata.ChunkCount, CreatedAt: metadata.CreatedAt, Target: currentTarget}
		}
		found = true
	}
	if !found {
		return Entry{}, false, nil
	}
	if len(keys) != 1 {
		return Entry{}, false, fmt.Errorf("knowledge: document %q exists in multiple writable scopes", docID)
	}
	var oldKey string
	for key := range keys {
		oldKey = key
	}
	owned := scope.Owning(target)
	newKey := scopeKey(owned)
	for i := range b.chunks {
		if b.chunks[i].docID != docID || b.chunks[i].scopeKey != oldKey {
			continue
		}
		metadata := ReadMetadata(b.chunks[i].metadata)
		b.chunks[i].scopeKey = newKey
		b.chunks[i].metadata = MetadataFor(owned, metadata.DocID, metadata.Title, metadata.Source, metadata.CreatedAt, metadata.Chunk, metadata.ChunkCount)
	}
	entry.Target = target
	return entry, true, nil
}

// Search implements KnowledgeBase and returns documents ordered by relevance.
func (b *InMemoryBase) Search(ctx context.Context, scope Scope, query string, topK int) ([]*schema.Document, error) {
	return b.search(ctx, scope, query, topK, nil)
}

// SearchFiltered applies a metadata filter before ranking and limiting. It is
// the optional FilteredKnowledgeBase extension used by fixed playbooks.
func (b *InMemoryBase) SearchFiltered(ctx context.Context, scope Scope, query string, topK int, filter func(Metadata) bool) ([]*schema.Document, error) {
	return b.search(ctx, scope, query, topK, filter)
}

func (b *InMemoryBase) search(ctx context.Context, scope Scope, query string, topK int, filter func(Metadata) bool) ([]*schema.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("knowledge: search query is required")
	}
	if topK <= 0 {
		topK = 4
	}
	var queryVector []float64
	if b.embedder != nil {
		vectors, err := b.embed(ctx, []string{query})
		if err != nil {
			return nil, fmt.Errorf("knowledge: embed query: %w", err)
		}
		queryVector = vectors[0]
	}
	type result struct {
		doc   *schema.Document
		score float64
		chunk int
	}
	readable := ReadableBy(scope)
	b.mu.RLock()
	results := make([]result, 0, len(b.chunks))
	for _, chunk := range b.chunks {
		metadata := ReadMetadata(chunk.metadata)
		if !readable(metadata) {
			continue
		}
		if filter != nil && !filter(metadata) {
			continue
		}
		score := lexicalScore(query, chunk.content)
		if queryVector != nil {
			score = cosine(queryVector, chunk.vector)
		}
		metadataCopy := cloneMetadata(chunk.metadata)
		document := &schema.Document{ID: chunk.docID, Content: chunk.content, MetaData: metadataCopy}
		document.WithScore(score)
		results = append(results, result{doc: document, score: score, chunk: metadata.Chunk})
	}
	b.mu.RUnlock()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			if results[i].doc.ID == results[j].doc.ID {
				return results[i].chunk < results[j].chunk
			}
			return results[i].doc.ID < results[j].doc.ID
		}
		return results[i].score > results[j].score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	documents := make([]*schema.Document, 0, len(results))
	for _, result := range results {
		documents = append(documents, result.doc)
	}
	return documents, nil
}

func scopeKey(scope Scope) string {
	scope = scope.normalized()
	return scope.Owner + "\x00" + scope.Group + "\x00" + scope.Tenant
}

func cloneMetadata(values map[string]any) map[string]any {
	copyOf := make(map[string]any, len(values)+1)
	for key, value := range values {
		copyOf[key] = value
	}
	return copyOf
}

func splitText(text string, size, overlap int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(runes)
	}
	if overlap >= size {
		overlap = 0
	}
	step := size - overlap
	parts := make([]string, 0, (len(runes)+step-1)/step)
	for start := 0; start < len(runes); start += step {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		part := strings.TrimSpace(string(runes[start:end]))
		if part != "" {
			parts = append(parts, part)
		}
		if end == len(runes) {
			break
		}
	}
	return parts
}

func lexicalScore(query, content string) float64 {
	queryTokens := tokenSet(query)
	contentTokens := tokenSet(content)
	if len(queryTokens) == 0 || len(contentTokens) == 0 {
		return 0
	}
	matched := 0
	for token := range queryTokens {
		if contentTokens[token] {
			matched++
		}
	}
	return float64(matched) / float64(len(queryTokens))
}

func tokenSet(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		result[token] = true
	}
	return result
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i := range a {
		dot += a[i] * b[i]
		aa += a[i] * a[i]
		bb += b[i] * b[i]
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}

package knowledge

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/storage"
	coretools "github.com/ishi-o/golem/core/tools"
)

const (
	ToolNameListKnowledge        = "ListKnowledgeBase"
	ToolNameIndexKnowledge       = "IndexKnowledge"
	ToolNameSearchKnowledge      = "SearchKnowledge"
	ToolNameMoveKnowledge        = "UpdateKnowledgeScope"
	ToolNameDeleteKnowledge      = "DeleteKnowledge"
	ToolNameListOwnerKnowledge   = "ListOwnerKnowledgeBase"
	ToolNameSearchOwnerKnowledge = "SearchOwnerKnowledge"
)

// Tools exposes intentional knowledge operations. Automatic retrieval belongs
// to the agent option; these tools are for writing, inspecting, and repairing
// a knowledge base when the model needs explicit control.
type Tools struct {
	base     KnowledgeBase
	home     storage.Home
	pageSize int
	topK     int
}

// ToolsOption configures model-facing knowledge tools.
type ToolsOption func(*Tools)

// WithPageSize sets the default listing page size, capped at 100.
func WithPageSize(size int) ToolsOption {
	return func(t *Tools) {
		if size > 0 {
			t.pageSize = size
		}
	}
}

// WithTopK sets the default explicit search size.
func WithTopK(topK int) ToolsOption {
	return func(t *Tools) {
		if topK > 0 {
			t.topK = topK
		}
	}
}

// NewTools constructs the knowledge tool family. A nil home is allowed for
// text indexing; path-based indexing is refused without a containment
// boundary. Remote fetching is intentionally outside this facade so a model
// cannot turn indexing into an SSRF primitive.
func NewTools(base KnowledgeBase, home storage.Home, options ...ToolsOption) *Tools {
	t := &Tools{base: base, home: home, pageSize: 20, topK: 4}
	for _, option := range options {
		if option != nil {
			option(t)
		}
	}
	if t.pageSize > 100 {
		t.pageSize = 100
	}
	return t
}

// List implements tools.Builtin.
func (t *Tools) List() []tool.InvokableTool {
	if t == nil || t.base == nil {
		return nil
	}
	return []tool.InvokableTool{t.list(), t.index(), t.search(), t.move(), t.delete()}
}

func (t *Tools) requestScope(ctx context.Context) (Scope, error) {
	owner, err := coretools.UserID.Require(ctx)
	if err != nil {
		return Scope{}, err
	}
	group, _ := coretools.GroupID.Get(ctx)
	tenant, _ := coretools.TenantID.Get(ctx)
	return NewScope(owner, group, tenant), nil
}

func (t *Tools) list() tool.InvokableTool {
	type input struct {
		Offset int `json:"offset,omitempty"`
		Limit  int `json:"limit,omitempty"`
	}
	type output struct {
		Entries []Entry `json:"entries"`
		HasMore bool    `json:"hasMore"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameListKnowledge,
		"List documents in the knowledge base visible to the current user. Results are one row per document, not per chunk.",
		func(ctx context.Context, in input) (output, error) {
			scope, err := t.requestScope(ctx)
			if err != nil {
				return output{}, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = t.pageSize
			}
			if limit > 100 {
				limit = 100
			}
			page, err := t.base.List(ctx, scope, maxZero(in.Offset), limit)
			if err != nil {
				return output{}, err
			}
			return output{Entries: page.Entries, HasMore: page.HasMore}, nil
		}))
}

func (t *Tools) index() tool.InvokableTool {
	type input struct {
		Title  string `json:"title"`
		Scope  string `json:"scope,omitempty"`
		Source string `json:"source,omitempty"`
		Text   string `json:"text,omitempty"`
		DocID  string `json:"docId"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameIndexKnowledge,
		"Add or replace a knowledge document. docId must identify the same source across revisions; scope is own, group, or tenant.",
		func(ctx context.Context, in input) (string, error) {
			scope, err := t.requestScope(ctx)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(in.Title) == "" {
				return "", fmt.Errorf("knowledge title is required")
			}
			if strings.TrimSpace(in.DocID) == "" {
				return "", fmt.Errorf("knowledge docId is required")
			}
			target := TargetOrOwn(in.Scope)
			if strings.TrimSpace(in.Scope) != "" {
				parsed, ok := ParseTarget(in.Scope)
				if !ok {
					return "", fmt.Errorf("unknown knowledge scope %q", in.Scope)
				}
				target = parsed
			}
			if !scope.Reachable(target) {
				return "", fmt.Errorf("knowledge scope %q is not available in this request", target)
			}
			if strings.TrimSpace(in.Text) == "" && strings.TrimSpace(in.Source) == "" {
				return "", fmt.Errorf("knowledge text or source is required")
			}
			if strings.TrimSpace(in.Text) == "" {
				if err := t.validateLocation(in.Source); err != nil {
					return "", err
				}
			}
			origin := strings.TrimSpace(in.Source)
			if origin == "" {
				origin, _ = coretools.ReplyMessageID.Get(ctx)
			}
			var source Source
			if strings.TrimSpace(in.Text) == "" {
				source = NewPathSource(scope, target, in.Title, origin, in.DocID)
			} else {
				source = NewTextSource(scope, target, in.Title, in.Text, origin, in.DocID)
			}
			id, err := t.base.Index(ctx, source)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("indexed knowledge document %q in %s (%s)", in.Title, target, id), nil
		}))
}

func (t *Tools) search() tool.InvokableTool {
	type input struct {
		Query string `json:"query"`
		TopK  int    `json:"topK,omitempty"`
	}
	type passage struct {
		ID      string   `json:"id"`
		Content string   `json:"content"`
		Score   float64  `json:"score"`
		Meta    Metadata `json:"metadata"`
	}
	type output struct {
		Passages []passage `json:"passages"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameSearchKnowledge,
		"Search visible knowledge passages by natural-language query. Automatic retrieval already runs on ordinary turns; use this for an intentional alternate query.",
		func(ctx context.Context, in input) (output, error) {
			if strings.TrimSpace(in.Query) == "" {
				return output{}, fmt.Errorf("knowledge query is required")
			}
			scope, err := t.requestScope(ctx)
			if err != nil {
				return output{}, err
			}
			topK := in.TopK
			if topK <= 0 {
				topK = t.topK
			}
			if topK > 20 {
				topK = 20
			}
			documents, err := t.base.Search(ctx, scope, in.Query, topK)
			if err != nil {
				return output{}, err
			}
			out := output{Passages: make([]passage, 0, len(documents))}
			for _, document := range documents {
				if document == nil {
					continue
				}
				out.Passages = append(out.Passages, passage{ID: document.ID, Content: document.Content, Score: document.Score(), Meta: ReadMetadata(document.MetaData)})
			}
			return out, nil
		}))
}

func (t *Tools) move() tool.InvokableTool {
	return coretools.MustTool(utils.InferTool(ToolNameMoveKnowledge,
		"Move a visible knowledge document to own, group, or tenant scope while retaining its content and id.",
		func(ctx context.Context, in struct {
			DocID string `json:"docId"`
			Scope string `json:"scope"`
		}) (string, error) {
			scope, err := t.requestScope(ctx)
			if err != nil {
				return "", err
			}
			target, ok := ParseTarget(in.Scope)
			if !ok {
				return "", fmt.Errorf("unknown knowledge scope %q", in.Scope)
			}
			if !scope.Reachable(target) {
				return "", fmt.Errorf("knowledge scope %q is not available in this request", target)
			}
			entry, found, err := t.base.Move(ctx, scope, strings.TrimSpace(in.DocID), target)
			if err != nil {
				return "", err
			}
			if !found {
				return "", fmt.Errorf("knowledge document %q was not found", in.DocID)
			}
			return fmt.Sprintf("moved knowledge document %q to %s", entry.Title, target), nil
		}))
}

func (t *Tools) delete() tool.InvokableTool {
	return coretools.MustTool(utils.InferTool(ToolNameDeleteKnowledge,
		"Delete a visible knowledge document and all of its chunks.",
		func(ctx context.Context, in struct {
			DocID string `json:"docId"`
		}) (string, error) {
			scope, err := t.requestScope(ctx)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(in.DocID) == "" {
				return "", fmt.Errorf("knowledge docId is required")
			}
			if err := t.base.Delete(ctx, scope, in.DocID); err != nil {
				return "", err
			}
			return "deleted knowledge document " + in.DocID, nil
		}))
}

func (t *Tools) validateLocation(location string) error {
	if t.home == nil {
		return fmt.Errorf("knowledge path indexing is unavailable without a workspace")
	}
	if parsed, err := url.Parse(location); err == nil && parsed.Scheme != "" {
		return fmt.Errorf("remote knowledge sources are not supported; provide text or a local path")
	}
	if !filepath.IsAbs(location) || !t.home.Contains(location) {
		return fmt.Errorf("knowledge source path is outside the available workspace")
	}
	return nil
}

// AdminTools is the explicit read-only escape hatch for unattended owners
// that nobody logs into. The authorization function is supplied by the
// embedding application; core never guesses who an administrator is.
type AdminTools struct {
	base     KnowledgeBase
	IsAdmin  func(context.Context) bool
	pageSize int
	topK     int
}

// NewAdminTools constructs administrator-only knowledge listing/search tools.
func NewAdminTools(base KnowledgeBase, isAdmin func(context.Context) bool) *AdminTools {
	return &AdminTools{base: base, IsAdmin: isAdmin, pageSize: 20, topK: 4}
}

// List implements tools.Builtin.
func (t *AdminTools) List() []tool.InvokableTool {
	if t == nil || t.base == nil || t.IsAdmin == nil {
		return nil
	}
	return []tool.InvokableTool{t.listOwner(), t.searchOwner()}
}

func (t *AdminTools) listOwner() tool.InvokableTool {
	type input struct {
		Owner  string `json:"owner"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameListOwnerKnowledge,
		"List the own-scope knowledge documents for another identity. Administrators only.",
		func(ctx context.Context, in input) (Page, error) {
			if !t.IsAdmin(ctx) {
				return Page{}, fmt.Errorf("administrator access is required")
			}
			if strings.TrimSpace(in.Owner) == "" {
				return Page{}, fmt.Errorf("knowledge owner is required")
			}
			limit := in.Limit
			if limit <= 0 {
				limit = t.pageSize
			}
			return t.base.List(ctx, NewScope(in.Owner, "", ""), maxZero(in.Offset), limit)
		}))
}

func (t *AdminTools) searchOwner() tool.InvokableTool {
	type input struct {
		Owner string `json:"owner"`
		Query string `json:"query"`
		TopK  int    `json:"topK,omitempty"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameSearchOwnerKnowledge,
		"Search the own-scope knowledge documents for another identity. Administrators only.",
		func(ctx context.Context, in input) ([]*schema.Document, error) {
			if !t.IsAdmin(ctx) {
				return nil, fmt.Errorf("administrator access is required")
			}
			if strings.TrimSpace(in.Owner) == "" || strings.TrimSpace(in.Query) == "" {
				return nil, fmt.Errorf("knowledge owner and query are required")
			}
			topK := in.TopK
			if topK <= 0 {
				topK = t.topK
			}
			return t.base.Search(ctx, NewScope(in.Owner, "", ""), in.Query, topK)
		}))
}

func maxZero(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

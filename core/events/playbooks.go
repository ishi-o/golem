package events

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/knowledge"
	coretools "github.com/ishi-o/golem/core/tools"
)

const (
	ToolNameListPlaybooks = coretools.ToolNameListPlaybooks
	ToolNameWritePlaybook = coretools.ToolNameWritePlaybook
)

// PlaybookTools exposes the administrator surface for the fixed knowledge
// used by event triage. It intentionally writes only to the configured
// source owner's personal scope; an administrator's own identity must never
// change where unattended triage reads its instructions from.
type PlaybookTools struct {
	base    knowledge.KnowledgeBase
	cfg     Config
	isAdmin func(context.Context) bool
}

// NewPlaybookTools constructs administrator-only playbook tools. The
// authorization function belongs to the embedding application because core
// cannot infer its account or role model.
func NewPlaybookTools(base knowledge.KnowledgeBase, cfg Config, isAdmin func(context.Context) bool) *PlaybookTools {
	return &PlaybookTools{base: base, cfg: cfg, isAdmin: isAdmin}
}

// List implements tools.Builtin.
func (t *PlaybookTools) List() []tool.InvokableTool {
	if t == nil || t.base == nil || t.isAdmin == nil {
		return nil
	}
	return []tool.InvokableTool{t.list(), t.write()}
}

func (t *PlaybookTools) list() tool.InvokableTool {
	type playbook struct {
		Source  string   `json:"source"`
		Enabled bool     `json:"enabled"`
		Owner   string   `json:"owner,omitempty"`
		Query   string   `json:"query,omitempty"`
		DocIDs  []string `json:"docIds,omitempty"`
	}
	type output struct {
		Playbooks []playbook `json:"playbooks"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameListPlaybooks,
		"List the configured event-triage knowledge playbooks. Administrators only.",
		func(ctx context.Context, _ struct{}) (output, error) {
			if !t.isAdmin(ctx) {
				return output{}, fmt.Errorf("administrator access is required")
			}
			sources := make([]string, 0, len(t.cfg.Sources))
			for source := range t.cfg.Sources {
				sources = append(sources, source)
			}
			sort.Strings(sources)
			out := output{Playbooks: make([]playbook, 0, len(sources))}
			for _, source := range sources {
				configured := t.cfg.Sources[source]
				policy, enabled := t.cfg.PolicyFor(source)
				value := configured.Playbook.normalized()
				if !value.HasQuery() && len(value.DocIDs) == 0 {
					value = t.cfg.Playbook.normalized()
				}
				owner := configured.Owner.UserID
				if enabled {
					value = policy.Playbook
					owner = policy.Owner.UserID
				}
				out.Playbooks = append(out.Playbooks, playbook{
					Source: source, Enabled: enabled, Owner: strings.TrimSpace(owner),
					Query: value.Query, DocIDs: append([]string(nil), value.DocIDs...),
				})
			}
			return out, nil
		}))
}

func (t *PlaybookTools) write() tool.InvokableTool {
	type input struct {
		Source string `json:"source"`
		DocID  string `json:"docId"`
		Title  string `json:"title"`
		Text   string `json:"text"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameWritePlaybook,
		"Write one event-triage playbook document for a configured source. Administrators only.",
		func(ctx context.Context, in input) (string, error) {
			if !t.isAdmin(ctx) {
				return "", fmt.Errorf("administrator access is required")
			}
			source := strings.TrimSpace(in.Source)
			if source == "" || strings.TrimSpace(in.DocID) == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Text) == "" {
				return "", fmt.Errorf("source, docId, title, and text are required")
			}
			policy, enabled := t.cfg.PolicyFor(source)
			if !enabled {
				return "", fmt.Errorf("event source %q is not enabled", source)
			}
			if !policy.Playbook.HasQuery() {
				return "", fmt.Errorf("event source %q has no playbook query", source)
			}
			docID := strings.TrimSpace(in.DocID)
			if !policy.Playbook.Accepts(docID) {
				return "", fmt.Errorf("document %q is not allowed by event source %q playbook", docID, source)
			}
			owner := strings.TrimSpace(policy.Owner.UserID)
			if owner == "" {
				return "", fmt.Errorf("event source %q has no playbook owner", source)
			}
			origin, _ := coretools.ReplyMessageID.Get(ctx)
			if strings.TrimSpace(origin) == "" {
				origin = "playbook:" + source
			}
			id, err := t.base.Index(ctx, knowledge.NewTextSource(
				knowledge.NewScope(owner, "", ""), knowledge.TargetOwn,
				in.Title, in.Text, origin, docID,
			))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("indexed playbook document %q for %s (%s)", in.Title, source, id), nil
		}))
}

package tools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/storage"
)

// Provider assembles the tool set of a run — the Go shape of spring-agent's
// AgentToolsProvider. Everything a run offers is composed here: the
// registered scenario tools (the Go equivalent of @AgentTool beans, filtered
// through Scenario.Offers), the per-user file, memory and skill tools, the
// todo and ask tools, publish, and the MCP servers the user can reach.
//
// Registration is process-wide and read per run; Compose is the only thing
// that touches per-user state.
type Provider struct {
	Config     config.Config
	Workspaces *storage.WorkspaceFactory
	// Repos supplies the resource repo the publish tools need; nil (a
	// harness without persistence) means the publish tools are not offered.
	Repos dao.Backend
	// MCP builds the run's MCP tools, nil when the deployment has none.
	MCP MCPBuilder
	// Interceptors wrap every tool the run offers; the large response
	// interceptor is added by NewProvider from the config.
	Interceptors []Interceptor
	Log          *slog.Logger

	mu         sync.Mutex
	registered []RegisteredTool
}

// RegisteredTool is one always-available tool, optionally gated by scenario.
type RegisteredTool struct {
	Tool tool.InvokableTool
	// Offers decides whether a run of this scenario gets the tool; nil
	// means every run does. This is spring-agent's Scenario.offers(tool).
	Offers func(name string) bool
}

// MCPBuilder is what the mcp package supplies to the provider: the servers
// a user can reach, connected, and a way to close them when the run ends.
// An interface defined here so the provider does not depend on the mcp
// package's details (or its client library) — only on the tools it yields.
type MCPBuilder interface {
	Build(ctx context.Context, userID, chatID string) (MCPTools, error)
}

// MCPTools is one run's MCP connections. Tools without the Closer would leak
// a handshake per run; the Closer without the tools would be pointless.
type MCPTools struct {
	Tools  []tool.InvokableTool
	Closer io.Closer
}

// NewProvider wires the provider and adds the built-in interceptor.
func NewProvider(cfg config.Config, workspaces *storage.WorkspaceFactory, repos dao.Backend, mcp MCPBuilder) *Provider {
	_ = cfg.Normalize()
	if workspaces == nil {
		workspaces = storage.NewWorkspaceFactory(cfg.Storage.Location)
	}
	return &Provider{
		Config:     cfg,
		Workspaces: workspaces,
		Repos:      repos,
		MCP:        mcp,
		Log:        slog.Default(),
		Interceptors: []Interceptor{
			&LargeResponseInterceptor{
				GuideThreshold: cfg.AI.GuideThreshold,
				Workspaces:     workspaces,
			},
		},
	}
}

// Register makes a tool available to runs. Call it during wiring, before
// the first Fire; offers gates it by scenario (see RegisteredTool).
func (p *Provider) Register(t tool.InvokableTool, offers func(name string) bool) {
	if t == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registered = append(p.registered, RegisteredTool{Tool: t, Offers: offers})
}

// ComposeRequest names what Compose needs from the run. ScenarioOffers is
// the resolved Scenario.Offers of the request; the rest is identity and the
// per-run handlers.
type ComposeRequest struct {
	ScenarioOffers func(name string) bool
	UserID         string
	ChatID         string
	TodoHandler    TodoEventHandler
	Questions      QuestionHandler
	// AnswersArriveLater is true when Questions cannot answer inline.
	AnswersArriveLater bool
	// AskedMessage is the ask tool's recorded instruction; see AskOptions.
	AskedMessage string
	// AskEnabled is the config gate on the ask tool.
	AskEnabled bool
}

// Composition is one run's tool set. Close must be called when the run
// ends, whatever it ended by; it closes the MCP connections the composition
// opened, and is safe to call when there are none.
type Composition struct {
	Tools []tool.InvokableTool
	Info  []*schema.ToolInfo
	Close func()
}

// Compose assembles the run's tools. It is called from the run goroutine,
// not the Fire caller: MCP handshakes are blocking network work, and the
// callers are event dispatchers with delivery deadlines (a channel that
// sees no acknowledgement concludes its event was lost and sends it again —
// the reason spring-agent subscribed on boundedElastic).
func (p *Provider) Compose(ctx context.Context, req ComposeRequest) (*Composition, error) {
	if p == nil {
		return nil, fmt.Errorf("golem/tools: nil provider")
	}
	if p.Workspaces == nil {
		p.Workspaces = storage.NewWorkspaceFactory(p.Config.Storage.Location)
	}
	var tools []tool.InvokableTool

	home := p.Workspaces.ForOwner(req.UserID)

	// The scenario tools, filtered. A name-resolved filter (rather than
	// passing tool values) keeps the Scenario interface free of this
	// package's types.
	p.mu.Lock()
	registered := make([]RegisteredTool, len(p.registered))
	copy(registered, p.registered)
	p.mu.Unlock()
	for _, e := range registered {
		if e.Offers != nil && req.ScenarioOffers != nil {
			info, err := e.Tool.Info(ctx)
			if err != nil || info == nil {
				continue
			}
			if !req.ScenarioOffers(info.Name) {
				continue
			}
		}
		tools = append(tools, e.Tool)
	}

	// The per-user tools. One FileSystemTools per run, rooted at the
	// workspace, because the root is who is asking.
	fs, err := NewFileSystemTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, fs.Tools()...)

	memories, err := NewMemoryTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, memories.Tools()...)

	skills, err := NewSkillTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, skills.Tools()...)

	tools = append(tools, CurrentDateTime(), TodoWrite(req.TodoHandler))

	// Publish needs the resource repo; a provider wired without persistence
	// (a test harness, say) simply does not offer it.
	if p.Repos != nil {
		publish := NewPublishFileTools(p.Repos.PublishedResources(), storage.NewFileSystem(p.Config.Storage.Location), home, p.Config.AI.Tools.PublishFile.BaseURL)
		tools = append(tools, publish.Tools()...)
	}

	if req.Questions != nil && req.AskEnabled {
		tools = append(tools, AskUserQuestion(req.Questions, AskOptions{
			AnswersArriveLater: req.AnswersArriveLater,
			AskedMessage:       req.AskedMessage,
		}))
	}

	comp := &Composition{Close: func() {}}

	// The MCP tools last: they are the ones with a lifecycle. A failing
	// server costs its tools, never the run — a monitoring server being
	// down must not take the agent's ability to answer with it.
	if p.MCP != nil {
		mcpTools, err := p.MCP.Build(ctx, req.UserID, req.ChatID)
		if err == nil {
			tools = append(tools, mcpTools.Tools...)
			inner := comp.Close
			closer := mcpTools.Closer
			comp.Close = func() {
				inner()
				if closer != nil {
					if err := closer.Close(); err != nil {
						p.logger().Error("closing MCP tools failed", "err", err)
					}
				}
			}
		} else if err != nil {
			p.logger().Warn("MCP tools unavailable for this run", "err", err)
		}
	}

	// Wrap with the interceptor chain and collect the model-facing info.
	comp.Tools = make([]tool.InvokableTool, 0, len(tools))
	comp.Info = make([]*schema.ToolInfo, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		wrapped := WrapTool(t, p.Interceptors...)
		info, err := wrapped.Info(ctx)
		if err != nil {
			comp.Close()
			return nil, fmt.Errorf("tool info: %w", err)
		}
		comp.Tools = append(comp.Tools, wrapped)
		comp.Info = append(comp.Info, info)
	}
	return comp, nil
}

func (p *Provider) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

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
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
)

// Provider assembles the tool set for a run. It combines registered tools,
// per-user file, memory and skill tools, todo and question tools, publishing,
// and any MCP servers available to the user.
//
// Registration is process-wide and read per run; Compose is the only thing
// that touches per-user state.
//
// This package must not import core/agent: the agent depends on the tools,
// so the tools cannot depend back on it (the schedule feature lives in
// core/schedule for exactly that reason).
type Provider struct {
	config     config.Config
	workspaces *storage.WorkspaceFactory
	// repos supplies the resource repo the publish tools need; nil (a
	// harness without persistence) means the publish tools are not offered.
	repos store.Backend
	// mcp builds the run's MCP tools, nil when the deployment has none.
	mcp MCPBuilder
	// interceptors wrap every tool the run offers; the large response
	// interceptor is added by NewProvider from the config.
	interceptors []Interceptor
	log          *slog.Logger

	mu         sync.RWMutex
	registered []registeredTool
}

type registeredTool struct {
	tool tool.InvokableTool
	// offers decides whether a run of this scenario gets the tool; nil
	// means every run does.
	offers func(name string) bool
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

// ProviderOption configures a Provider during construction.
type ProviderOption func(*Provider)

// WithLogger supplies the logger used for optional-tool failures.
func WithLogger(log *slog.Logger) ProviderOption {
	return func(p *Provider) {
		if log != nil {
			p.log = log
		}
	}
}

// WithInterceptor adds an interceptor to the provider's tool chain.
func WithInterceptor(interceptor Interceptor) ProviderOption {
	return func(p *Provider) {
		if interceptor != nil {
			p.interceptors = append(p.interceptors, interceptor)
		}
	}
}

// NewProvider constructs the provider and adds the built-in interceptor.
func NewProvider(cfg config.Config, workspaces *storage.WorkspaceFactory, repos store.Backend, mcp MCPBuilder, options ...ProviderOption) *Provider {
	_ = cfg.Normalize()
	if workspaces == nil {
		workspaces = storage.NewWorkspaceFactory(cfg.Storage.Location)
	}
	p := &Provider{
		config:     cfg,
		workspaces: workspaces,
		repos:      repos,
		mcp:        mcp,
		log:        slog.Default(),
		interceptors: []Interceptor{
			&LargeResponseInterceptor{
				GuideThreshold: cfg.AI.GuideThreshold,
				Workspaces:     workspaces,
			},
		},
	}
	for _, option := range options {
		if option != nil {
			option(p)
		}
	}
	return p
}

// Register makes a tool available to runs. Call it during application setup,
// before the first Fire; offers gates it by scenario.
func (p *Provider) Register(t tool.InvokableTool, offers func(name string) bool) {
	if t == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registered = append(p.registered, registeredTool{tool: t, offers: offers})
}

// RegisterSandbox adds the common shell tools for one optional sandbox
// backend. The backend remains owned by the application and should be closed
// during application shutdown.
func (p *Provider) RegisterSandbox(sandbox Sandbox, config SandboxToolsConfig) error {
	if p == nil {
		return fmt.Errorf("golem/tools: nil provider")
	}
	if config.Credentials == nil && p.repos != nil {
		config.Credentials = p.repos.ShellCredentials()
	}
	registered, err := NewSandboxTools(sandbox, config)
	if err != nil {
		return err
	}
	for _, t := range registered.List() {
		p.Register(t, nil)
	}
	return nil
}

// ComposeRequest names what Compose needs from the run. ScenarioOffers is
// the resolved Scenario.Offers of the request; the rest is identity and the
// per-run handlers. GroupID and TenantID, when set, widen the run's reads:
// the group's and tenant's homes join the user's own for the file, skill
// and publish tools.
type ComposeRequest struct {
	ScenarioOffers func(name string) bool
	UserID         string
	ChatID         string
	GroupID        string
	TenantID       string
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
// callers may be event dispatchers with delivery deadlines, so connection
// setup remains inside the run rather than blocking the event receiver.
func (p *Provider) Compose(ctx context.Context, req ComposeRequest) (*Composition, error) {
	if p == nil {
		return nil, fmt.Errorf("golem/tools: nil provider")
	}
	var tools []tool.InvokableTool

	// The run's view of the directories its tools work in: the user's own
	// home, plus the group's and tenant's for reading when the run carries
	// those scopes. Writes always belong to the user — what a run produces
	// belongs to who asked.
	var home storage.Home = p.workspaces.ForOwner(req.UserID)
	if req.GroupID != "" || req.TenantID != "" {
		members := []storage.Home{}
		if req.GroupID != "" {
			members = append(members, p.workspaces.ForGroup(req.GroupID))
		}
		if req.TenantID != "" {
			members = append(members, p.workspaces.ForTenant(req.TenantID))
		}
		home = storage.NewCompositeHome(home, members...)
	}

	// The scenario tools, filtered. A name-resolved filter (rather than
	// passing tool values) keeps the Scenario interface free of this
	// package's types.
	p.mu.RLock()
	registered := make([]registeredTool, len(p.registered))
	copy(registered, p.registered)
	p.mu.RUnlock()
	for _, e := range registered {
		if req.ScenarioOffers != nil {
			info, err := e.tool.Info(ctx)
			if err != nil || info == nil {
				continue
			}
			// The scenario decides first; a registered offers is an
			// additional restriction, not a bypass of the scenario's.
			if !req.ScenarioOffers(info.Name) {
				continue
			}
			if e.offers != nil && !e.offers(info.Name) {
				continue
			}
		}
		tools = append(tools, e.tool)
	}

	// The per-user tools. One FileSystemTools per run, rooted at the
	// workspace, because the root is who is asking.
	fs, err := NewFileSystemTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, fs.List()...)

	memories, err := NewMemoryTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, memories.List()...)

	skills, err := NewSkillTools(home)
	if err != nil {
		return nil, err
	}
	tools = append(tools, skills.List()...)

	tools = append(tools, NewCurrentDateTimeTools().List()...)
	tools = append(tools, NewTodoWriteTools(req.TodoHandler).List()...)

	// Publish needs the resource repo; a provider configured without persistence
	// (a test harness, say) simply does not offer it.
	if p.repos != nil {
		publish := NewPublishFileTools(p.repos.PublishedResources(), storage.NewFileSystem(p.config.Storage.Location), home, p.config.AI.Tools.PublishFile.BaseURL)
		tools = append(tools, publish.List()...)
	}

	if req.Questions != nil && req.AskEnabled {
		tools = append(tools, NewAskUserQuestionTools(req.Questions, AskOptions{
			AnswersArriveLater: req.AnswersArriveLater,
			AskedMessage:       req.AskedMessage,
		}).List()...)
	}

	comp := &Composition{Close: func() {}}

	// The MCP tools last: they are the ones with a lifecycle. A failing
	// server costs its tools, never the run — a monitoring server being
	// down must not take the agent's ability to answer with it.
	if p.mcp != nil {
		mcpTools, err := p.mcp.Build(ctx, req.UserID, req.ChatID)
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
		wrapped := WrapTool(t, p.interceptors...)
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
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

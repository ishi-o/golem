// Package config is the runtime configuration: plain structs, populated by
// the embedder (from environment variables, a file, or code) with defaults
// supplied here. No binder framework — spring-agent bound these through
// @ConfigurationProperties, and the Go port's equivalent of that machinery is
// the Load function's explicit os.Getenv calls, which stay greppable and
// dependency-free.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultSystemPrompt is what the agent is told when an application states no
// prompt of its own. Written to suit any surface: it names no chat, no
// terminal and no tool that is not part of core, so an integration overrides
// it to add its own rules rather than to restate these.

// DefaultGuideThreshold is the tool-result size above which the large
// response interceptor diverts the result to a file in the user's workspace.
// 30000 characters is roughly where a tool result starts crowding the context
// window more than it earns.
const DefaultGuideThreshold = 30000

// Config is the whole runtime configuration. Zero values are normalized by
// Normalize, which every embedder must call before handing the config to the
// runtime; the one field spring-agent's normalizer forgot to default cost
// every application that did not configure it a nil pointer from the
// interceptor — not at startup, but on the first tool call of the first turn.
type Config struct {
	// Locale names the language the agent writes its own words in (bundle
	// selection), as a BCP 47 tag: "en", "zh-CN". Empty means the process
	// default.
	Locale string

	Storage Storage
	AI      AI
}

// Storage locates the file storage and the URLs published links point at.
type Storage struct {
	// Location is the root directory every user's workspace lives under.
	Location string
	// BaseURL is the origin share URLs are built from, e.g. https://host.
	BaseURL string
	// CdnURL optionally replaces BaseURL in links when set.
	CdnURL string
}

// AI is the model-facing configuration.
type AI struct {
	// SystemPrompt and ScheduledTaskPrompt are templates rendered per run;
	// empty means the defaults above.
	SystemPrompt        string
	ScheduledTaskPrompt string

	// Admins are user ids the runtime treats as privileged, comma-separated
	// in the environment.
	Admins []string

	// ModelPricing prices models per million tokens, for the usage footers
	// surfaces render.
	ModelPricing map[string]ModelPricing

	// GuideThreshold is the tool-result size the large response interceptor
	// diverts at; 0 means DefaultGuideThreshold.
	GuideThreshold int

	Tools Tools
}

// ModelPricing is what one model costs, per million tokens, in the currency
// named.
type ModelPricing struct {
	NonThinkingInputPerMillion float64
	ThinkingInputPerMillion    float64
	OutputPerMillion           float64
	Currency                   string
}

// Tools configures the built-in tools.
type Tools struct {
	AskUserQuestion AskUserQuestion
	PublishFile     PublishFile
	Mcp             Mcp
	ToolSearch      ToolSearch
}

// AskUserQuestion configures the ask tool. The TTL bounds how long a
// question stays answerable; Feishu caps it at 14 days regardless, the life
// of a card entity.
type AskUserQuestion struct {
	Enabled bool
	TTL     time.Duration
}

// PublishFile carries the origin publish links point at. Distinct from
// Storage.BaseURL: this is what the tool prints, that is what the share
// handler checks.
type PublishFile struct {
	BaseURL string
}

// Mcp configures the MCP client factory.
type Mcp struct {
	// TrustedHosts is the SSRF-guard allowlist: hosts allowed to serve MCP
	// over plain http, and to resolve to addresses the guard would otherwise
	// reject (a local monitoring stack, say).
	TrustedHosts []string
}

// ToolSearch configures the tool-search index. MaxResults bounds how many
// tools one search materializes; EnableThreshold is the MCP tool count above
// which a run hides its tools behind the search tool instead of sending every
// definition to the model on every call.
type ToolSearch struct {
	MaxResults      int
	EnableThreshold int
}

// Normalize fills in defaults and is idempotent. It returns an error rather
// than silently fixing contradictory input.
func (c *Config) Normalize() error {
	if strings.TrimSpace(c.AI.SystemPrompt) == "" {
		c.AI.SystemPrompt = DefaultSystemPrompt
	}
	if strings.TrimSpace(c.AI.ScheduledTaskPrompt) == "" {
		c.AI.ScheduledTaskPrompt = DefaultScheduledTaskPrompt
	}
	if c.AI.GuideThreshold <= 0 {
		c.AI.GuideThreshold = DefaultGuideThreshold
	}
	if c.AI.Tools.AskUserQuestion.TTL <= 0 {
		// 24h, Feishu's own card life being the practical ceiling anyway.
		c.AI.Tools.AskUserQuestion.TTL = 24 * time.Hour
	}
	if c.AI.Tools.ToolSearch.MaxResults <= 0 {
		c.AI.Tools.ToolSearch.MaxResults = 5
	}
	if c.AI.Tools.ToolSearch.EnableThreshold <= 0 {
		c.AI.Tools.ToolSearch.EnableThreshold = 20
	}
	if c.Storage.Location == "" {
		c.Storage.Location = "data"
	}
	return nil
}

// Load builds a Config from environment variables — the same variables
// spring-agent reads, so a deployment's env carries over — and normalizes it.
// Variables nobody set are simply absent; the two lists below are the whole
// surface, documented once each:
//
//	GOLEM_LOCALE              agent's own words (en, zh-CN)
//	GOLEM_STORAGE_LOCATION    file storage root            (default "data")
//	GOLEM_STORAGE_BASE_URL    origin share URLs are built from
//	GOLEM_STORAGE_CDN_URL     optional CDN origin
//	GOLEM_ADMINS              comma-separated privileged user ids
//	GOLEM_ASK_USER_TTL        how long a question stays answerable (Go duration)
//	GOLEM_PUBLISH_BASE_URL    origin publish links point at
//	GOLEM_GUIDE_THRESHOLD     tool-result divert threshold in characters
//	GOLEM_TOOL_SEARCH_RESULTS tool-search max results
//	GOLEM_MCP_TRUSTED_HOSTS   comma-separated SSRF-guard allowlist
func Load() (Config, error) {
	c := Config{
		Locale: os.Getenv("GOLEM_LOCALE"),
		Storage: Storage{
			Location: os.Getenv("GOLEM_STORAGE_LOCATION"),
			BaseURL:  os.Getenv("GOLEM_STORAGE_BASE_URL"),
			CdnURL:   os.Getenv("GOLEM_STORAGE_CDN_URL"),
		},
		AI: AI{
			Admins:         splitList(os.Getenv("GOLEM_ADMINS")),
			GuideThreshold: envInt("GOLEM_GUIDE_THRESHOLD"),
			Tools: Tools{
				AskUserQuestion: AskUserQuestion{
					Enabled: envBool("GOLEM_ASK_USER_ENABLED", true),
					TTL:     envDuration("GOLEM_ASK_USER_TTL"),
				},
				PublishFile: PublishFile{BaseURL: os.Getenv("GOLEM_PUBLISH_BASE_URL")},
				Mcp:         Mcp{TrustedHosts: splitList(os.Getenv("GOLEM_MCP_TRUSTED_HOSTS"))},
				ToolSearch: ToolSearch{
					MaxResults:      envInt("GOLEM_TOOL_SEARCH_RESULTS"),
					EnableThreshold: envInt("GOLEM_TOOL_SEARCH_THRESHOLD"),
				},
			},
		},
	}
	if err := c.Normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(name string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(name string) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func envDuration(name string) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// String renders the config for a log line, safe to print: no secrets live
// here, which is itself a property worth keeping — secrets belong to model
// clients and credential stores, not to the runtime config.
func (c Config) String() string {
	return fmt.Sprintf("config{locale=%s storage=%s admins=%d guideThreshold=%d askTTL=%s}",
		c.Locale, c.Storage.Location, len(c.AI.Admins), c.AI.GuideThreshold, c.AI.Tools.AskUserQuestion.TTL)
}

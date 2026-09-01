// Package config contains the runtime configuration: plain structs that
// embedders populate from environment variables, a file, or application
// code, normalized by Normalize. The environment-backed default (the GOLEM_*
// variables) lives in the golem-cli repository's bootstrap, next to the
// driver glue it assembles.
package config

import (
	"fmt"
	"strings"
	"time"
)

// DefaultSystemPrompt is what the agent is told when an application states no
// prompt of its own. Written to suit any surface: it names no chat, no
// terminal and no tool that is not part of core, so an integration overrides
// it to add its own rules rather than to restate these.

// DefaultGuideThreshold is the tool-result size above which the large
// response middleware diverts the result to a file in the user's workspace.
// 30000 characters is roughly where a tool result starts crowding the context
// window more than it earns.
const DefaultGuideThreshold = 30000

// Config is the whole runtime configuration. Zero values are normalized by
// Normalize, which every application must call before handing the config to the
// runtime. Normalize fills defaults and validates values before the runtime
// starts.
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
	// SubagentPrompt wraps the brief a subagent was started with; empty
	// means DefaultSubagentPrompt.
	SubagentPrompt string

	// Admins are user ids the runtime treats as privileged, comma-separated
	// in the environment.
	Admins []string

	// ModelPricing prices models per million tokens, for the usage footers
	// surfaces render.
	ModelPricing map[string]ModelPricing

	// GuideThreshold is the tool-result size the large response middleware
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
	MCP             MCP
	Subagent        Subagent
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

// MCP configures the MCP client factory.
type MCP struct {
	// TrustedHosts is the SSRF-guard allowlist: hosts allowed to serve MCP
	// over plain http, and to resolve to addresses the guard would otherwise
	// reject (a local monitoring stack, say).
	TrustedHosts []string
}

// Subagent configures the subagent tools (core/subagent). MaxConcurrent
// bounds the subagents one run may have in flight at once; WaitPoll bounds
// one WaitForSubagent call before it hands the turn back to the model, which
// is what stops a wait from holding a turn for good; WaitTimeout is the age
// at which a subagent is treated as faulted rather than slow and cancelled —
// it also bounds how long a parent run is held open for its children, so it
// must stay within the agent's shutdown drain window.
type Subagent struct {
	MaxConcurrent int
	WaitPoll      time.Duration
	WaitTimeout   time.Duration
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
	if strings.TrimSpace(c.AI.SubagentPrompt) == "" {
		c.AI.SubagentPrompt = DefaultSubagentPrompt
	}
	if c.AI.GuideThreshold <= 0 {
		c.AI.GuideThreshold = DefaultGuideThreshold
	}
	if c.AI.Tools.AskUserQuestion.TTL <= 0 {
		// 24h, Feishu's own card life being the practical ceiling anyway.
		c.AI.Tools.AskUserQuestion.TTL = 24 * time.Hour
	}
	if c.AI.Tools.Subagent.MaxConcurrent <= 0 {
		c.AI.Tools.Subagent.MaxConcurrent = 10
	}
	if c.AI.Tools.Subagent.WaitPoll <= 0 {
		c.AI.Tools.Subagent.WaitPoll = time.Minute
	}
	if c.AI.Tools.Subagent.WaitTimeout <= 0 {
		// Within the agent's ten-minute shutdown drain: a parent waiting on
		// a straggler counts as in-flight.
		c.AI.Tools.Subagent.WaitTimeout = 10 * time.Minute
	}
	if c.Storage.Location == "" {
		c.Storage.Location = "data"
	}
	return nil
}

// String renders the config for a log line, safe to print: no secrets live
// here, which is itself a property worth keeping — secrets belong to model
// clients and credential stores, not to the runtime config.
func (c Config) String() string {
	return fmt.Sprintf("config{locale=%s storage=%s admins=%d guideThreshold=%d askTTL=%s}",
		c.Locale, c.Storage.Location, len(c.AI.Admins), c.AI.GuideThreshold, c.AI.Tools.AskUserQuestion.TTL)
}

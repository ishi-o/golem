package bootstrap

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/config"
)

// Load builds a Config from the GOLEM_* environment variables and normalizes
// it — the environment-backed default for this repository's applications;
// embedders populate the core structs however they like.
//
// Variables nobody set are simply absent; the list below is the whole
// surface, documented once:
//
//	GOLEM_LOCALE                agent's own words (en, zh-CN)
//	GOLEM_STORAGE_LOCATION      file storage root            (default "data")
//	GOLEM_STORAGE_BASE_URL      origin share URLs are built from
//	GOLEM_STORAGE_CDN_URL       optional CDN origin
//	GOLEM_ADMINS                comma-separated privileged user ids
//	GOLEM_ASK_USER_ENABLED      offer the ask tool            (default true)
//	GOLEM_ASK_USER_TTL          how long a question stays answerable (Go duration)
//	GOLEM_PUBLISH_BASE_URL      origin publish links point at
//	GOLEM_GUIDE_THRESHOLD       tool-result divert threshold in characters
//	GOLEM_TOOL_SEARCH_RESULTS   tool-search max results
//	GOLEM_TOOL_SEARCH_THRESHOLD tool-search enable threshold
//	GOLEM_MCP_TRUSTED_HOSTS     comma-separated SSRF-guard allowlist
func Load() (config.Config, error) {
	c := config.Config{
		Locale: os.Getenv("GOLEM_LOCALE"),
		Storage: config.Storage{
			Location: os.Getenv("GOLEM_STORAGE_LOCATION"),
			BaseURL:  os.Getenv("GOLEM_STORAGE_BASE_URL"),
			CdnURL:   os.Getenv("GOLEM_STORAGE_CDN_URL"),
		},
		AI: config.AI{
			Admins:         splitList(os.Getenv("GOLEM_ADMINS")),
			GuideThreshold: envInt("GOLEM_GUIDE_THRESHOLD"),
			Tools: config.Tools{
				AskUserQuestion: config.AskUserQuestion{
					Enabled: envBool("GOLEM_ASK_USER_ENABLED", true),
					TTL:     envDuration("GOLEM_ASK_USER_TTL"),
				},
				PublishFile: config.PublishFile{BaseURL: os.Getenv("GOLEM_PUBLISH_BASE_URL")},
				MCP:         config.MCP{TrustedHosts: splitList(os.Getenv("GOLEM_MCP_TRUSTED_HOSTS"))},
				ToolSearch: config.ToolSearch{
					MaxResults:      envInt("GOLEM_TOOL_SEARCH_RESULTS"),
					EnableThreshold: envInt("GOLEM_TOOL_SEARCH_THRESHOLD"),
				},
			},
		},
	}
	if err := c.Normalize(); err != nil {
		return config.Config{}, err
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

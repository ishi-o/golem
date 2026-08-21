// Package toolsearch indexes tool descriptions so a run with more MCP tools
// than the model should see can offer a search instead of every definition
// — the Go port of spring-agent's tool-search index, deliberately cut down:
// in-heap vectors over an embeddings interface, keyed per user (a user's
// servers are the same across their conversations), mirrored to a JSON file
// so a restart does not re-embed everything.
package toolsearch

import (
	"context"
)

// Entry is one indexed tool.
type Entry struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// Index is the search index over tool descriptions.
type Index interface {
	// Replace re-indexes a user's tools wholesale: the caller holds the
	// full set (the servers reachable just resolved), and diffing here
	// would re-derive what the caller already knows.
	Replace(ctx context.Context, key string, entries []Entry) error
	// Search returns the top n entries for a query.
	Search(ctx context.Context, key, query string, n int) ([]Entry, error)
}

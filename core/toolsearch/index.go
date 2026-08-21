// Package toolsearch indexes tool descriptions so a run with many MCP tools
// can offer a search instead of sending every definition to the model. It
// uses in-memory vectors keyed per user and persists the index as JSON in the
// user's workspace.
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

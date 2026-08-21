// Package daocontract is the behaviour every persistence backend must hold,
// run once per backend — the Go equivalent of spring-agent's
// AbstractPersistenceBackendTest, which it subclassed once per backend with
// a distinct owner id to avoid unique-constraint collisions.
//
// The suite is a library, not a test of this module: the repository's central
// ./test module builds each backend fixture (a SQLite file, a Mongo container,
// a Redis container) and calls Run. A behaviour that must hold for every
// backend goes here, once, rather than into an adapter package's test.
package daocontract

import (
	"testing"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
)

// Fixture is one backend under test, fresh per Run call.
type Fixture interface {
	// Backend returns the repository bundle under test.
	Backend() dao.Backend

	// Memory returns the chat memory store under test, sharing the backend's
	// database.
	Memory() chatmemory.Repository

	// Owner returns a user id unique to this fixture instance, so unique
	// constraints (owner+name) cannot collide across backends running
	// against the same server.
	Owner() string

	// Close releases the fixture's resources.
	Close() error
}

// Run asserts the whole persistence contract against a fresh fixture.
func Run(t *testing.T, f Fixture) {
	t.Run("McpServerConfig", func(t *testing.T) { testMcpServerConfig(t, f) })
	t.Run("McpServerConfigAccess", func(t *testing.T) { testMcpServerConfigAccess(t, f) })
	t.Run("PendingQuestion", func(t *testing.T) { testPendingQuestion(t, f) })
	t.Run("PublishedResource", func(t *testing.T) { testPublishedResource(t, f) })
	t.Run("ScheduledTask", func(t *testing.T) { testScheduledTask(t, f) })
	t.Run("ShellCredential", func(t *testing.T) { testShellCredential(t, f) })
	t.Run("ProcessedMessage", func(t *testing.T) { testProcessedMessage(t, f) })
	t.Run("ChatMemory", func(t *testing.T) { testChatMemory(t, f) })
}

package sqlx

import (
	"context"

	"github.com/ishi-o/golem/core/store"
)

// The repository views expose the core contracts while keeping SQL-specific
// persistence methods private to this package.
type mcpStore struct{ store *Store }

func (r mcpStore) Save(ctx context.Context, value store.MCPServerConfig) error {
	return r.store.saveMCP(ctx, value)
}
func (r mcpStore) ListByOwner(ctx context.Context, ownerID string) ([]store.MCPServerConfig, error) {
	return r.store.listMCPByOwner(ctx, ownerID)
}
func (r mcpStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.MCPServerConfig, error) {
	return r.store.getMCPByOwnerAndName(ctx, ownerID, name)
}
func (r mcpStore) ExistsByOwnerAndName(ctx context.Context, ownerID, name string) (bool, error) {
	return r.store.existsMCPByOwnerAndName(ctx, ownerID, name)
}
func (r mcpStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
	return r.store.deleteMCPByOwnerAndName(ctx, ownerID, name)
}
func (r mcpStore) ListSharedWith(ctx context.Context, identifiers []string) ([]store.MCPServerConfig, error) {
	return r.store.listMCPSharedWith(ctx, identifiers)
}
func (r mcpStore) ListAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]store.MCPServerConfig, error) {
	return r.store.listMCPAccessibleTo(ctx, ownerID, identifiers)
}

type pendingStore struct{ store *Store }

func (r pendingStore) Save(ctx context.Context, value store.PendingQuestion) error {
	return r.store.savePendingQuestion(ctx, value)
}
func (r pendingStore) Get(ctx context.Context, id string) (*store.PendingQuestion, error) {
	return r.store.getPending(ctx, id)
}
func (r pendingStore) ListByConversationAndStatus(ctx context.Context, conversationID string, status store.PendingQuestionStatus) ([]store.PendingQuestion, error) {
	return r.store.listPendingByConversationAndStatus(ctx, conversationID, status)
}
func (r pendingStore) SetStatus(ctx context.Context, id string, status store.PendingQuestionStatus) error {
	return r.store.setPendingStatus(ctx, id, status)
}

type resourceStore struct{ store *Store }

func (r resourceStore) Save(ctx context.Context, value store.PublishedResource) error {
	return r.store.savePublishedResource(ctx, value)
}
func (r resourceStore) Get(ctx context.Context, id string) (*store.PublishedResource, error) {
	return r.store.findPublishedResource(ctx, id)
}
func (r resourceStore) Delete(ctx context.Context, id string) error {
	return r.store.deletePublishedResource(ctx, id)
}

type taskStore struct{ store *Store }

func (r taskStore) Save(ctx context.Context, value store.ScheduledTask) error {
	return r.store.saveScheduledTask(ctx, value)
}
func (r taskStore) Get(ctx context.Context, id string) (*store.ScheduledTask, error) {
	return r.store.findScheduledTask(ctx, id)
}
func (r taskStore) ListByStatus(ctx context.Context, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return r.store.listTasksByStatus(ctx, status)
}
func (r taskStore) ListByUserAndStatus(ctx context.Context, userID string, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return r.store.listTasksByUserAndStatus(ctx, userID, status)
}
func (r taskStore) SetStatus(ctx context.Context, id string, status store.ScheduledTaskStatus) error {
	return r.store.updateTaskStatus(ctx, id, status)
}

type credentialStore struct{ store *Store }

func (r credentialStore) Save(ctx context.Context, value store.ShellCredential) error {
	return r.store.saveShellCredential(ctx, value)
}
func (r credentialStore) ListByOwner(ctx context.Context, ownerID string) ([]store.ShellCredential, error) {
	return r.store.listCredentialsByOwner(ctx, ownerID)
}
func (r credentialStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.ShellCredential, error) {
	return r.store.getCredentialByOwnerAndName(ctx, ownerID, name)
}
func (r credentialStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
	return r.store.deleteCredentialByOwnerAndName(ctx, ownerID, name)
}

type processedStore struct{ store *Store }

func (r processedStore) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.claim(ctx, id)
}
func (r processedStore) Release(ctx context.Context, id string) error {
	return r.store.release(ctx, id)
}

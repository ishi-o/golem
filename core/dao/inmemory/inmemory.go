// Package inmemory is a whole Backend plus chat memory in process memory —
// the fourth backend, deliberately not one of the pluggable ones. It exists
// for tests (the agent runtime's tests run against it, no container
// required) and for the harnesses that run before persistence is wired. It
// is not a cache with a TTL and not durable: stop the process and
// everything is gone.
package inmemory

import (
	"context"
	"sort"
	"sync"

	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
)

// Backend implements dao.Backend and chatmemory.Repository.
type Backend struct {
	mu sync.Mutex

	mcpServers    map[string]dao.McpServerConfig
	questions     map[string]dao.PendingQuestion
	resources     map[string]dao.PublishedResource
	tasks         map[string]dao.ScheduledTask
	credentials   map[string]dao.ShellCredential
	claims        map[string]bool
	conversations map[string][]*schema.Message
}

// New builds an empty backend.
func New() *Backend {
	return &Backend{
		mcpServers:    map[string]dao.McpServerConfig{},
		questions:     map[string]dao.PendingQuestion{},
		resources:     map[string]dao.PublishedResource{},
		tasks:         map[string]dao.ScheduledTask{},
		credentials:   map[string]dao.ShellCredential{},
		claims:        map[string]bool{},
		conversations: map[string][]*schema.Message{},
	}
}

func (b *Backend) McpServerConfigs() dao.McpServerConfigRepo     { return mcpRepo{b} }
func (b *Backend) PendingQuestions() dao.PendingQuestionRepo     { return questionRepo{b} }
func (b *Backend) PublishedResources() dao.PublishedResourceRepo { return resourceRepo{b} }
func (b *Backend) ScheduledTasks() dao.ScheduledTaskRepo         { return taskRepo{b} }
func (b *Backend) ShellCredentials() dao.ShellCredentialRepo     { return credRepo{b} }
func (b *Backend) ProcessedMessages() dao.ProcessedMessageRepo   { return claimRepo{b} }

// Memory returns the backend as its chat memory store.
func (b *Backend) Memory() chatmemory.Repository { return memoryRepo{b} }

// --- MCP servers ---

type mcpRepo struct{ *Backend }

func (r mcpRepo) Save(_ context.Context, c dao.McpServerConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mcpServers[c.ID] = c
	return nil
}

func (r mcpRepo) list(filter func(dao.McpServerConfig) bool) []dao.McpServerConfig {
	out := make([]dao.McpServerConfig, 0, len(r.mcpServers))
	for _, c := range r.mcpServers {
		if filter(c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r mcpRepo) FindByOwnerID(_ context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(func(c dao.McpServerConfig) bool { return c.OwnerID == ownerID }), nil
}

func (r mcpRepo) FindByOwnerIDAndName(_ context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.mcpServers {
		if c.OwnerID == ownerID && c.Name == name {
			cc := c
			return &cc, nil
		}
	}
	return nil, nil
}

func (r mcpRepo) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	found, err := r.FindByOwnerIDAndName(ctx, ownerID, name)
	return found != nil, err
}

func (r mcpRepo) DeleteByOwnerIDAndName(_ context.Context, ownerID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.mcpServers {
		if c.OwnerID == ownerID && c.Name == name {
			delete(r.mcpServers, id)
			return nil
		}
	}
	return nil
}

func (r mcpRepo) FindBySharedWithIn(_ context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	set := map[string]bool{}
	for _, i := range identifiers {
		set[i] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(func(c dao.McpServerConfig) bool {
		for _, s := range c.SharedWith {
			if set[s] {
				return true
			}
		}
		return false
	}), nil
}

func (r mcpRepo) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	shared, err := r.FindBySharedWithIn(ctx, identifiers)
	if err != nil {
		return nil, err
	}
	owned, err := r.FindByOwnerID(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []dao.McpServerConfig
	for _, c := range append(owned, shared...) {
		if !seen[c.ID] {
			seen[c.ID] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// --- Pending questions ---

type questionRepo struct{ *Backend }

func (r questionRepo) Save(_ context.Context, q dao.PendingQuestion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.questions[q.ID] = q
	return nil
}

func (r questionRepo) FindByID(_ context.Context, id string) (*dao.PendingQuestion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.questions[id]; ok {
		qq := q
		return &qq, nil
	}
	return nil, nil
}

func (r questionRepo) FindByConversationIDAndStatus(_ context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []dao.PendingQuestion
	for _, q := range r.questions {
		if q.ConversationID == conversationID && q.Status == status {
			out = append(out, q)
		}
	}
	return out, nil
}

func (r questionRepo) UpdateStatus(_ context.Context, id string, status dao.PendingQuestionStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.questions[id]; ok {
		q.Status = status
		r.questions[id] = q
	}
	return nil
}

// --- Published resources ---

type resourceRepo struct{ *Backend }

func (r resourceRepo) Save(_ context.Context, p dao.PublishedResource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resources[p.ID] = p
	return nil
}

func (r resourceRepo) FindByID(_ context.Context, id string) (*dao.PublishedResource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.resources[id]; ok {
		pp := p
		return &pp, nil
	}
	return nil, nil
}

func (r resourceRepo) DeleteByID(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.resources, id)
	return nil
}

// --- Scheduled tasks ---

type taskRepo struct{ *Backend }

func (r taskRepo) Save(_ context.Context, t dao.ScheduledTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.ID] = t
	return nil
}

func (r taskRepo) FindByID(_ context.Context, id string) (*dao.ScheduledTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		tt := t
		return &tt, nil
	}
	return nil, nil
}

func (r taskRepo) findBy(filter func(dao.ScheduledTask) bool) []dao.ScheduledTask {
	out := make([]dao.ScheduledTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		if filter(t) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r taskRepo) FindByStatus(_ context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findBy(func(t dao.ScheduledTask) bool { return t.Status == status }), nil
}

func (r taskRepo) FindByUserIDAndStatus(_ context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findBy(func(t dao.ScheduledTask) bool { return t.UserID == userID && t.Status == status }), nil
}

func (r taskRepo) UpdateStatus(_ context.Context, id string, status dao.ScheduledTaskStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.Status = status
		r.tasks[id] = t
	}
	return nil
}

// --- Shell credentials ---

type credRepo struct{ *Backend }

func (r credRepo) Save(_ context.Context, c dao.ShellCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentials[c.ID] = c
	return nil
}

func (r credRepo) FindByOwnerID(_ context.Context, ownerID string) ([]dao.ShellCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]dao.ShellCredential, 0)
	for _, c := range r.credentials {
		if c.OwnerID == ownerID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r credRepo) FindByOwnerIDAndName(_ context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.credentials {
		if c.OwnerID == ownerID && c.Name == name {
			cc := c
			return &cc, nil
		}
	}
	return nil, nil
}

func (r credRepo) DeleteByOwnerIDAndName(_ context.Context, ownerID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.credentials {
		if c.OwnerID == ownerID && c.Name == name {
			delete(r.credentials, id)
			return nil
		}
	}
	return nil
}

// --- Processed messages ---

type claimRepo struct{ *Backend }

// Claim is atomic under the backend's one lock — the property the contract
// asks for, held here by construction rather than by a conditional write.
func (r claimRepo) Claim(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims[id] {
		return false, nil
	}
	r.claims[id] = true
	return true, nil
}

func (r claimRepo) Release(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.claims, id)
	return nil
}

// --- Chat memory ---

type memoryRepo struct{ *Backend }

func (r memoryRepo) Append(_ context.Context, conversationID string, messages []*schema.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range messages {
		r.conversations[conversationID] = append(r.conversations[conversationID], m)
	}
	return nil
}

func (r memoryRepo) Load(_ context.Context, conversationID string, window int) ([]*schema.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	history := r.conversations[conversationID]
	if window > 0 && len(history) > window {
		history = history[len(history)-window:]
	}
	out := make([]*schema.Message, len(history))
	for i, m := range history {
		cp := *m
		out[i] = &cp
	}
	return out, nil
}

func (r memoryRepo) Delete(_ context.Context, conversationID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conversations, conversationID)
	return nil
}

func (r memoryRepo) ListConversations(_ context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.conversations))
	for id := range r.conversations {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

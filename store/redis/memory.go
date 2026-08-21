package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/schema"
)

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	encodedMessages := make([]any, 0, len(messages))
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("encode chat message: %w", err)
		}
		encodedMessages = append(encodedMessages, string(data))
	}
	pipe := s.client.TxPipeline()
	pipe.RPush(ctx, s.record("memory", conversationID), encodedMessages...)
	pipe.SAdd(ctx, s.all("memory"), conversationID)
	_, err := pipe.Exec(ctx)
	return wrap("append chat memory", err)
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	start := int64(0)
	if window > 0 {
		start = -int64(window)
	}
	values, err := s.client.LRange(ctx, s.record("memory", conversationID), start, -1).Result()
	if err != nil {
		return nil, wrap("load chat memory", err)
	}
	out := make([]*schema.Message, 0, len(values))
	for _, value := range values {
		var message schema.Message
		if err := json.Unmarshal([]byte(value), &message); err != nil {
			return nil, fmt.Errorf("decode chat message: %w", err)
		}
		out = append(out, &message)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.record("memory", conversationID))
	pipe.SRem(ctx, s.all("memory"), conversationID)
	_, err := pipe.Exec(ctx)
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	ids, err := s.client.SMembers(ctx, s.all("memory")).Result()
	if err != nil {
		return nil, wrap("list chat conversations", err)
	}
	sort.Strings(ids)
	return ids, nil
}

package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/schema"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type memoryDocument struct {
	ID       string   `bson:"_id"`
	Messages []string `bson:"messages"`
}

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	encoded := make([]string, 0, len(messages))
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("encode chat message: %w", err)
		}
		encoded = append(encoded, string(data))
	}
	_, err := s.collection("chat_memory").UpdateOne(ctx, bson.M{"_id": conversationID}, bson.M{"$setOnInsert": bson.M{"_id": conversationID}, "$push": bson.M{"messages": bson.M{"$each": encoded}}}, options.Update().SetUpsert(true))
	return wrap("append chat memory", err)
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	var document memoryDocument
	err := s.collection("chat_memory").FindOne(ctx, bson.M{"_id": conversationID}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return []*schema.Message{}, nil
	}
	if err != nil {
		return nil, wrap("load chat memory", err)
	}
	start := 0
	if window > 0 && len(document.Messages) > window {
		start = len(document.Messages) - window
	}
	out := make([]*schema.Message, 0, len(document.Messages)-start)
	for _, encoded := range document.Messages[start:] {
		var message schema.Message
		if err := json.Unmarshal([]byte(encoded), &message); err != nil {
			return nil, fmt.Errorf("decode chat message: %w", err)
		}
		out = append(out, &message)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	_, err := s.collection("chat_memory").DeleteOne(ctx, bson.M{"_id": conversationID})
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	cursor, err := s.collection("chat_memory").Find(ctx, bson.D{}, options.Find().SetProjection(bson.M{"_id": 1}).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("list chat conversations", err)
	}
	defer cursor.Close(ctx)
	var documents []memoryDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode chat conversations", err)
	}
	ids := make([]string, 0, len(documents))
	for _, document := range documents {
		ids = append(ids, document.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

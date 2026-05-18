package chatstate

import (
	"encoding/json"
	"fmt"
	"sync"

	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/database"
	coderdpubsub "github.com/coder/coder/v2/coderd/pubsub"
)

// Publisher is the minimal interface chatstate needs to publish
// pubsub messages. It is intentionally compatible with
// database/pubsub.Pubsub: real callers pass the live pubsub directly,
// tests pass a recording fake, and callers that compose multiple
// machines inside one outer transaction pass a [*PublishBuffer].
type Publisher interface {
	Publish(event string, message []byte) error
}

// PublishBuffer is a [Publisher] that records each Publish call in
// order without forwarding it. Callers that want pubsub messages to
// land only after their own outer transaction commits should pass a
// PublishBuffer when constructing [ChatMachine]s, then invoke
// [PublishBuffer.Flush] only after the outer commit succeeds.
type PublishBuffer struct {
	inner Publisher

	mu       sync.Mutex
	pending  []bufferedMessage
	flushed  bool
	disabled bool
}

type bufferedMessage struct {
	Channel string
	Payload []byte
}

// NewPublishBuffer constructs a PublishBuffer that, when flushed, will
// forward messages in order to inner.
func NewPublishBuffer(inner Publisher) *PublishBuffer {
	return &PublishBuffer{inner: inner}
}

// Publish records a message. It never forwards to the inner publisher
// until [PublishBuffer.Flush] is called. Returns an error if Flush has
// already happened to make accidental reuse obvious.
func (b *PublishBuffer) Publish(channel string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return xerrors.Errorf("publish buffer flushed; cannot accept message for %q", channel)
	}
	if b.disabled {
		return nil
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	b.pending = append(b.pending, bufferedMessage{Channel: channel, Payload: cp})
	return nil
}

// Flush forwards every pending message to the inner publisher in the
// order it was buffered, then marks the buffer flushed. The first
// publish error is returned with the channel name annotated; later
// messages still attempt to publish so the inner publisher sees a
// consistent state.
func (b *PublishBuffer) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return nil
	}
	b.flushed = true
	var firstErr error
	for _, msg := range b.pending {
		if err := b.inner.Publish(msg.Channel, msg.Payload); err != nil && firstErr == nil {
			firstErr = xerrors.Errorf("publish %s: %w", msg.Channel, err)
		}
	}
	return firstErr
}

// Discard clears the buffered messages without forwarding them. Use
// it when an outer transaction rolls back.
func (b *PublishBuffer) Discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = nil
	b.disabled = true
}

// pending returns a snapshot of the buffered messages, primarily for
// tests via [PublishBuffer.BufferedChannels]. The returned slice is a
// copy and safe to inspect without holding the buffer lock.
func (b *PublishBuffer) snapshotPending() []bufferedMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]bufferedMessage, len(b.pending))
	copy(out, b.pending)
	return out
}

// BufferedChannels returns just the channels of the pending messages
// in order. Primarily useful for assertions in tests.
func (b *PublishBuffer) BufferedChannels() []string {
	pending := b.snapshotPending()
	out := make([]string, len(pending))
	for i, m := range pending {
		out[i] = m.Channel
	}
	return out
}

// buildChatUpdateMessage produces the JSON payload for a
// `chat:update:{chat_id}` message describing the post-transition
// snapshot of chat.
func buildChatUpdateMessage(chat database.Chat) []byte {
	msg := coderdpubsub.ChatStateUpdateMessage{
		SnapshotVersion:   chat.SnapshotVersion,
		HistoryVersion:    chat.HistoryVersion,
		QueueVersion:      chat.QueueVersion,
		GenerationAttempt: chat.GenerationAttempt,
		Status:            string(chat.Status),
		Archived:          chat.Archived,
	}
	if chat.WorkerID.Valid {
		id := chat.WorkerID.UUID
		msg.WorkerID = &id
	}
	if chat.RunnerID.Valid {
		id := chat.RunnerID.UUID
		msg.RunnerID = &id
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		// json.Marshal on this struct is total; panic is acceptable
		// because the only failure mode would be a bug in this
		// package, not user input.
		panic(fmt.Sprintf("marshal chat state update: %v", err))
	}
	return payload
}

// buildChatOwnershipMessage produces the JSON payload for the global
// `chat:ownership` ownership hint for chat.
func buildChatOwnershipMessage(chat database.Chat) []byte {
	payload, err := json.Marshal(coderdpubsub.ChatStateOwnershipMessage{
		ChatID:          chat.ID,
		SnapshotVersion: chat.SnapshotVersion,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal chat state ownership: %v", err))
	}
	return payload
}

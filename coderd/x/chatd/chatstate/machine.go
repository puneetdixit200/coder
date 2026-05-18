package chatstate

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/database"
	coderdpubsub "github.com/coder/coder/v2/coderd/pubsub"
)

// HeartbeatStaleSeconds is the threshold chatstate uses when deciding
// whether to publish a `chat:ownership` hint for a runnable chat. A
// heartbeat older than this many seconds (by database time) counts
// as stale and triggers a hint so an idle worker can attempt a
// takeover.
const HeartbeatStaleSeconds = 30

// Options configures a [ChatMachine]. Reserved for future tunables;
// currently empty.
type Options struct{}

// ChatMachine is a chat-scoped handle for state-machine operations on
// a single chat row. It captures the database store, the pubsub
// publisher, and the chat ID at construction time so callers do not
// have to thread them through Update, Lock, or any transition method.
//
// ChatMachine values are cheap. Create one per chat for the lifetime
// of a request or worker turn; do not cache mutable chat state across
// calls.
type ChatMachine struct {
	store     database.Store
	publisher Publisher
	chatID    uuid.UUID
	opts      Options
}

// NewChatMachine constructs a chat-scoped state machine handle. The
// store may be the root database handle or an existing transaction
// handle; publisher is the pubsub used for `chat:update` and
// `chat:ownership` emissions. Both are required and captured for the
// lifetime of the returned machine.
func NewChatMachine(
	store database.Store,
	publisher Publisher,
	chatID uuid.UUID,
	opts Options,
) *ChatMachine {
	return &ChatMachine{
		store:     store,
		publisher: publisher,
		chatID:    chatID,
		opts:      opts,
	}
}

// ChatID returns the chat ID this machine is scoped to.
func (m *ChatMachine) ChatID() uuid.UUID { return m.chatID }

// Tx is the per-transaction handle passed to [ChatMachine.Update]
// callbacks. It carries the active context, the transactional store,
// and the chat ID. Tx does not cache mutable chat state across calls:
// every transition method reads the chat row and queue cardinality
// from the database on entry, so a bundle of transitions inside one
// Update callback always validates against the latest committed state.
type Tx struct {
	ctx    context.Context
	store  database.Store
	chatID uuid.UUID
}

// Ctx returns the context the surrounding [ChatMachine.Update] call
// is using.
func (tx *Tx) Ctx() context.Context { return tx.ctx }

// ChatID returns the chat ID this transaction is scoped to.
func (tx *Tx) ChatID() uuid.UUID { return tx.chatID }

// Store exposes the active transaction store so callers can perform
// validation reads (for example loading the messages affected by an
// EditMessage transition) and metadata writes (for example updating
// title or labels) that must be atomic with the transition.
//
// Callers MUST NOT use Store to mutate execution-state tables
// (chats.status, chat_messages, chat_queued_messages, chat_heartbeats,
// or the version fields on chats). Those mutations belong to the
// transition methods and are validated against the state machine
// matrix.
func (tx *Tx) Store() database.Store { return tx.store }

// loadState reads the current chat row and queue cardinality from the
// active transaction, classifies the execution state, and returns the
// inputs every transition method needs. Returns ErrChatNotFound if
// the chat row was deleted in this transaction (or never existed).
func (tx *Tx) loadState() (database.Chat, ExecutionState, error) {
	chat, err := tx.store.GetChatByID(tx.ctx, tx.chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return database.Chat{}, StateN, ErrChatNotFound
	}
	if err != nil {
		return database.Chat{}, "", xerrors.Errorf("load chat: %w", err)
	}
	count, err := tx.store.CountChatQueuedMessages(tx.ctx, tx.chatID)
	if err != nil {
		return database.Chat{}, "", xerrors.Errorf("count queued messages: %w", err)
	}
	return chat, ClassifyExecutionState(chat, count > 0, true), nil
}

// requireFromAllowed loads the current state and validates t against
// the transition matrix. Returns the loaded chat and execution state
// on success, [ErrInvalidState] when the chat is in an invalid state
// and t is not [TransitionReconcileInvalidState], and a typed
// *TransitionError otherwise.
func (tx *Tx) requireFromAllowed(t Transition) (database.Chat, ExecutionState, error) {
	chat, from, err := tx.loadState()
	if err != nil {
		return chat, from, err
	}
	if from == StateInvalid && t != TransitionReconcileInvalidState {
		return chat, from, ErrInvalidState
	}
	if err := requireExecutionTransition(t, from); err != nil {
		return chat, from, err
	}
	return chat, from, nil
}

// Update applies one or more transitions to the machine's chat.
//
// Update uses the store and publisher captured by [NewChatMachine].
// It opens (or reuses) a transaction on the captured store,
// atomically locks the chat row with FOR UPDATE and increments
// `snapshot_version` exactly once, then runs fn against a fresh
// [*Tx]. Every successful commit publishes one `chat:update` message
// describing the committed post-callback chat. If the final
// execution state is runnable and ownership is missing or stale,
// Update also publishes one `chat:ownership` hint.
//
// Callers must not pass a store or publisher here; they belong on
// the machine.
//
// If the chat row does not exist, Update returns [ErrChatNotFound]
// without mutating anything.
//
// Callbacks that return an error roll back the transaction (rolling
// back the automatic snapshot bump) and publish nothing.
func (m *ChatMachine) Update(
	ctx context.Context,
	fn func(*Tx) error,
) error {
	if m.store == nil {
		return xerrors.New("chatstate: ChatMachine has nil store")
	}
	if m.publisher == nil {
		return xerrors.New("chatstate: ChatMachine has nil publisher")
	}

	var (
		finalChat      database.Chat
		finalChatExist bool
		finalState     ExecutionState
	)

	err := m.store.InTx(func(store database.Store) error {
		// Lock + bump in a single round trip.
		if _, err := store.LockChatAndBumpSnapshotVersion(ctx, m.chatID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrChatNotFound
			}
			return xerrors.Errorf("lock chat and bump snapshot: %w", err)
		}
		tx := &Tx{
			ctx:    ctx,
			store:  store,
			chatID: m.chatID,
		}
		if cberr := fn(tx); cberr != nil {
			return cberr
		}
		// Re-read the committed post-callback chat so publication uses
		// real database values (including anything the callback wrote
		// via tx.Store()).
		chat, state, lerr := tx.loadState()
		if lerr != nil {
			return lerr
		}
		finalChat = chat
		finalChatExist = true
		finalState = state
		return nil
	}, nil)
	if err != nil {
		return err
	}
	if !finalChatExist {
		return ErrChatNotFound
	}

	// Publish the committed update.
	updateChannel := coderdpubsub.ChatStateUpdateChannel(finalChat.ID)
	if perr := m.publisher.Publish(updateChannel, buildChatUpdateMessage(finalChat)); perr != nil {
		return xerrors.Errorf("publish %s: %w", updateChannel, perr)
	}

	// Maybe publish ownership hint. The heartbeat staleness check uses
	// database time so different replica clocks don't matter.
	if finalState.IsRunnable() {
		stale, herr := ownershipStaleOrMissing(ctx, m.store, finalChat, HeartbeatStaleSeconds)
		if herr != nil {
			return xerrors.Errorf("evaluate ownership: %w", herr)
		}
		if stale {
			if perr := m.publisher.Publish(coderdpubsub.ChatStateOwnershipChannel, buildChatOwnershipMessage(finalChat)); perr != nil {
				return xerrors.Errorf("publish ownership: %w", perr)
			}
		}
	}
	return nil
}

// Lock locks the chat row with FOR UPDATE and runs fn in a
// transaction without advancing snapshot_version. It uses the store
// captured by [NewChatMachine]. Use it when the caller needs a
// consistent chat snapshot plus related rows such as messages or
// queued messages but is NOT applying a transition.
//
// Callers must not pass a store here; it belongs on the machine.
//
// Lock publishes nothing. Callback errors roll back the transaction
// and propagate to the caller.
func (m *ChatMachine) Lock(
	ctx context.Context,
	fn func(database.Store) error,
) error {
	if m.store == nil {
		return xerrors.New("chatstate: ChatMachine has nil store")
	}
	return m.store.InTx(func(store database.Store) error {
		// GetChatByIDForUpdate locks the row WITHOUT bumping snapshot.
		_, err := store.GetChatByIDForUpdate(ctx, m.chatID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrChatNotFound
		}
		if err != nil {
			return xerrors.Errorf("lock chat: %w", err)
		}
		return fn(store)
	}, nil)
}

// ownershipStaleOrMissing reports whether the chat's current
// (chat_id, runner_id) lease is missing or stale. The staleSeconds
// threshold is forwarded to [database.IsChatHeartbeatStale] so the
// comparison runs against database time inside a single SQL query.
func ownershipStaleOrMissing(ctx context.Context, store database.Store, chat database.Chat, staleSeconds int32) (bool, error) {
	if !chat.WorkerID.Valid || !chat.RunnerID.Valid {
		return true, nil
	}
	return store.IsChatHeartbeatStale(ctx, database.IsChatHeartbeatStaleParams{
		ChatID:       chat.ID,
		RunnerID:     chat.RunnerID.UUID,
		StaleSeconds: staleSeconds,
	})
}

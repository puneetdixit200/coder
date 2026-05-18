// Package chatstate owns the durable execution-state transitions for
// the chatd subsystem. It implements the core state machine described
// in the chatd RFC: a 13-state execution model plus a 2-state
// ownership model on top of database rows in `chats`,
// `chat_messages`, `chat_queued_messages`, and `chat_heartbeats`.
//
// The package exposes two top-level entry points:
//
//   - [CreateChat] creates a brand new chat with its initial history
//     in a single transaction. It is standalone because no chat-scoped
//     state machine instance can exist before the chat row is written.
//   - [ChatMachine] wraps an existing chat. Callers use it to apply
//     one or more transitions atomically via [ChatMachine.Update], or
//     to read related rows while holding the chat row lock via
//     [ChatMachine.Lock].
//
// Every successful [ChatMachine.Update] call locks the chat row,
// advances `snapshot_version` exactly once, applies transition methods
// in order, and (on commit) publishes a single typed `chat:update`
// pubsub message describing the post-transition snapshot. Optional
// `chat:ownership` hints are published only when the post-transition
// state is runnable and ownership is missing or stale.
//
// Transition methods are explicit, typed wrappers around the SQL
// mutations needed to move between states. Each transition reads the
// current chat row and queue cardinality, classifies the resulting
// execution state, and rejects with an [*TransitionError] wrapping
// [ErrTransitionNotAllowed] when the transition is not legal from
// that state. The transition matrix and
// state classification helpers live in `state.go` and `transition.go`
// alongside unit-testable classifiers; the SQL is in
// `coderd/database/queries/chats.sql` (e.g. `LockChatAndBumpSnapshotVersion`,
// `UpdateChatExecutionState`).
//
// This package is the foundation introduced by PR 1 of the chatd
// refactor. HTTP handlers, the worker loop, and the streaming endpoint
// continue to use the legacy chatd code paths in the same PR; later
// PRs migrate them onto these transitions one by one.
package chatstate

package chatstate_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/sqlc-dev/pqtype"
	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/x/chatd/chatstate"
	"github.com/coder/coder/v2/testutil"
)

// TestTransitionAbandon_ClearsOwnership verifies the Acquire/Abandon
// round-trip: after Acquire the chat carries a worker+runner and a
// fresh heartbeat row exists, and after Abandon both ownership fields
// are cleared. The heartbeat row is not deleted by Abandon; heartbeat
// cleanup is a separate concern.
func TestTransitionAbandon_ClearsOwnership(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := testutil.Context(t, testutil.WaitShort)
	created := createTestChat(t, f)
	m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})

	worker := uuid.New()
	runner := uuid.New()

	// Acquire writes ownership and a fresh heartbeat row.
	require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
		_, err := tx.Acquire(chatstate.AcquireInput{WorkerID: worker, RunnerID: runner})
		return err
	}))
	owned := f.readChat(ctx, t, created.Chat.ID)
	require.Equal(t, worker, owned.WorkerID.UUID)
	require.Equal(t, runner, owned.RunnerID.UUID)
	hb, err := f.DB.GetChatHeartbeat(ctx, database.GetChatHeartbeatParams{
		ChatID:   created.Chat.ID,
		RunnerID: runner,
	})
	require.NoError(t, err, "Acquire writes a fresh heartbeat row")
	require.Equal(t, runner, hb.RunnerID)

	// Abandon clears ownership but leaves the heartbeat row intact.
	require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
		_, err := tx.Abandon(chatstate.AbandonInput{})
		return err
	}))
	hb, err = f.DB.GetChatHeartbeat(ctx, database.GetChatHeartbeatParams{
		ChatID:   created.Chat.ID,
		RunnerID: runner,
	})
	require.NoError(t, err, "Abandon does not delete the heartbeat row")
	abandoned := f.readChat(ctx, t, created.Chat.ID)
	require.False(t, abandoned.WorkerID.Valid, "Abandon clears worker_id")
	require.False(t, abandoned.RunnerID.Valid, "Abandon clears runner_id")
}

// TestTransitionAcquire_OverwritesFreshOwnership verifies that Acquire
// is an unconditional ownership handoff: a second worker calling
// Acquire on a chat that was *just* acquired by another worker
// successfully replaces ownership without inspecting heartbeat
// freshness. It also asserts that Acquire itself does not request an
// ownership hint, so the post-commit publish stays quiet on
// `chat:ownership` when the resulting heartbeat is fresh.
func TestTransitionAcquire_OverwritesFreshOwnership(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := testutil.Context(t, testutil.WaitShort)
	created := createTestChat(t, f)
	m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})

	firstWorker := uuid.New()
	firstRunner := uuid.New()
	require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
		_, err := tx.Acquire(chatstate.AcquireInput{WorkerID: firstWorker, RunnerID: firstRunner})
		return err
	}))

	// The chat is now owned with a fresh (chat_id, firstRunner)
	// heartbeat written by the first Acquire.
	firstChat := f.readChat(ctx, t, created.Chat.ID)
	require.Equal(t, firstWorker, firstChat.WorkerID.UUID)
	require.Equal(t, firstRunner, firstChat.RunnerID.UUID)
	_, err := f.DB.GetChatHeartbeat(ctx, database.GetChatHeartbeatParams{
		ChatID:   created.Chat.ID,
		RunnerID: firstRunner,
	})
	require.NoError(t, err, "first Acquire wrote a fresh heartbeat")
	// Sanity check: heartbeat is not stale by the same threshold the
	// machine uses for ownership-hint decisions.
	stale, err := f.DB.IsChatHeartbeatStale(ctx, database.IsChatHeartbeatStaleParams{
		ChatID:       created.Chat.ID,
		RunnerID:     firstRunner,
		StaleSeconds: chatstate.HeartbeatStaleSeconds,
	})
	require.NoError(t, err)
	require.False(t, stale, "first runner's heartbeat is fresh before the second Acquire")

	// Snapshot publish counts before the takeover so we can assert
	// Acquire does not publish an ownership hint itself.
	ownershipBefore := f.Pub.ownershipPublishCount()
	beforeChat := f.readChat(ctx, t, created.Chat.ID)

	secondWorker := uuid.New()
	secondRunner := uuid.New()
	require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
		_, err := tx.Acquire(chatstate.AcquireInput{WorkerID: secondWorker, RunnerID: secondRunner})
		return err
	}))

	after := f.readChat(ctx, t, created.Chat.ID)
	require.Equal(t, secondWorker, after.WorkerID.UUID, "ownership replaced")
	require.Equal(t, secondRunner, after.RunnerID.UUID, "runner replaced")
	require.Equal(t, beforeChat.SnapshotVersion+1, after.SnapshotVersion, "snapshot bumps exactly once")
	f.Pub.expectChatUpdate(t, created.Chat.ID, after.SnapshotVersion)

	// The new (chat_id, secondRunner) heartbeat exists. The old
	// (chat_id, firstRunner) row may or may not exist; Acquire is not
	// responsible for cleaning it up.
	_, err = f.DB.GetChatHeartbeat(ctx, database.GetChatHeartbeatParams{
		ChatID:   created.Chat.ID,
		RunnerID: secondRunner,
	})
	require.NoError(t, err, "second Acquire wrote a heartbeat for the new runner")

	// Acquire does not publish an ownership hint when it writes a fresh
	// heartbeat. The post-commit ownership-hint logic in Update stays
	// quiet because the new heartbeat is fresh, so no `chat:ownership`
	// notification fires.
	require.Equal(t, ownershipBefore, f.Pub.ownershipPublishCount(),
		"Acquire must not publish an ownership hint when the resulting heartbeat is fresh")
}

// TestTransitionAcquire_ExecutionStateOrthogonal verifies that Acquire
// preserves every execution-state field on the chat across
// representative valid execution states, including idle, runnable, and
// archived states. The transition only mutates ownership.
func TestTransitionAcquire_ExecutionStateOrthogonal(t *testing.T) {
	t.Parallel()

	// Each setup leaves the chat in the named state and returns the
	// chat ID for downstream assertions.
	cases := []struct {
		name  string
		state chatstate.ExecutionState
		setup func(t *testing.T, f *testFixture) uuid.UUID
	}{
		{
			name:  "R0",
			state: chatstate.StateR0,
			setup: func(t *testing.T, f *testFixture) uuid.UUID {
				return createTestChat(t, f).Chat.ID
			},
		},
		{
			name:  "W",
			state: chatstate.StateW,
			setup: func(t *testing.T, f *testFixture) uuid.UUID {
				created := createTestChat(t, f)
				ctx := testutil.Context(t, testutil.WaitShort)
				m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})
				require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
					_, err := tx.FinishTurn(chatstate.FinishTurnInput{})
					return err
				}))
				return created.Chat.ID
			},
		},
		{
			name:  "E0",
			state: chatstate.StateE0,
			setup: func(t *testing.T, f *testFixture) uuid.UUID {
				created := createTestChat(t, f)
				ctx := testutil.Context(t, testutil.WaitShort)
				m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})
				require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
					_, err := tx.FinishError(chatstate.FinishErrorInput{
						LastError: pqtype.NullRawMessage{
							RawMessage: json.RawMessage(`{"message":"boom"}`),
							Valid:      true,
						},
					})
					return err
				}))
				return created.Chat.ID
			},
		},
		{
			name:  "I0",
			state: chatstate.StateI0,
			setup: func(t *testing.T, f *testFixture) uuid.UUID {
				created := createTestChat(t, f)
				ctx := testutil.Context(t, testutil.WaitShort)
				m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})
				require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
					_, err := tx.Interrupt(chatstate.InterruptInput{Reason: "test"})
					return err
				}))
				return created.Chat.ID
			},
		},
		{
			name:  "XW",
			state: chatstate.StateXW,
			setup: func(t *testing.T, f *testFixture) uuid.UUID {
				created := createTestChat(t, f)
				ctx := testutil.Context(t, testutil.WaitShort)
				m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})
				require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
					_, err := tx.FinishTurn(chatstate.FinishTurnInput{})
					return err
				}))
				require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
					_, err := tx.SetArchived(chatstate.SetArchivedInput{Archived: true})
					return err
				}))
				return created.Chat.ID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newTestFixture(t)
			ctx := testutil.Context(t, testutil.WaitShort)
			chatID := tc.setup(t, f)
			require.Equal(t, tc.state, f.classify(ctx, t, chatID), "test setup must leave chat in %s", tc.state)

			before := f.readChat(ctx, t, chatID)
			queueBefore, err := f.DB.CountChatQueuedMessages(ctx, chatID)
			require.NoError(t, err)
			historyBefore := historyMessageIDs(ctx, t, f, chatID)

			worker := uuid.New()
			runner := uuid.New()
			m := chatstate.NewChatMachine(f.DB, f.Pub, chatID, chatstate.Options{})
			require.NoError(t, m.Update(ctx, func(tx *chatstate.Tx) error {
				_, err := tx.Acquire(chatstate.AcquireInput{WorkerID: worker, RunnerID: runner})
				return err
			}))

			after := f.readChat(ctx, t, chatID)
			// Ownership updated.
			require.Equal(t, worker, after.WorkerID.UUID)
			require.Equal(t, runner, after.RunnerID.UUID)
			// Execution state preserved.
			require.Equal(t, before.Status, after.Status, "status preserved")
			require.Equal(t, before.Archived, after.Archived, "archived flag preserved")
			require.Equal(t, before.RequiresActionDeadlineAt, after.RequiresActionDeadlineAt, "requires-action deadline preserved")
			require.Equal(t, before.LastError, after.LastError, "last_error preserved")
			require.Equal(t, before.HistoryVersion, after.HistoryVersion, "history_version preserved")
			require.Equal(t, before.QueueVersion, after.QueueVersion, "queue_version preserved")
			require.Equal(t, before.GenerationAttempt, after.GenerationAttempt, "generation_attempt preserved")
			// Classified state unchanged.
			require.Equal(t, tc.state, f.classify(ctx, t, chatID), "execution state preserved by Acquire")
			// Queue and history rows untouched.
			queueAfter, err := f.DB.CountChatQueuedMessages(ctx, chatID)
			require.NoError(t, err)
			require.Equal(t, queueBefore, queueAfter, "queue cardinality preserved")
			require.Equal(t, historyBefore, historyMessageIDs(ctx, t, f, chatID), "history preserved")
		})
	}
}

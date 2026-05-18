package chatstate_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/x/chatd/chatstate"
	"github.com/coder/coder/v2/testutil"
)

// waitForChan returns true if c receives a value before ctx is done.
// Helper used in concurrency tests to avoid time.Sleep.
func waitForChan(ctx context.Context, c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-ctx.Done():
		return false
	}
}

// stillBlocked returns true if c has NOT received a value yet. The
// caller must already have established a happens-before ordering via
// some other channel so this check is meaningful.
func stillBlocked(c <-chan struct{}) bool {
	select {
	case <-c:
		return false
	default:
		return true
	}
}

// TestLockLocksChatRow verifies that ChatMachine.Lock holds the chat
// row's FOR UPDATE lock until the callback returns, so a concurrent
// ChatMachine.Update cannot enter its callback until the Lock
// callback releases.
func TestLockLocksChatRow(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := testutil.Context(t, testutil.WaitMedium)
	created := createTestChat(t, f)
	m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})

	lockEntered := make(chan struct{})
	releaseLock := make(chan struct{})
	updateEntered := make(chan struct{})

	// Goroutine A: hold a Lock and block.
	var lockErr error
	var lockWG sync.WaitGroup
	lockWG.Add(1)
	go func() {
		defer lockWG.Done()
		lockErr = m.Lock(ctx, func(_ database.Store) error {
			close(lockEntered)
			<-releaseLock
			return nil
		})
	}()

	// Wait until A is inside its Lock callback (and therefore holds
	// the FOR UPDATE lock).
	require.True(t, waitForChan(ctx, lockEntered), "Lock callback never started")

	// Goroutine B: try to Update the same chat. It must block on
	// LockChatAndBumpSnapshotVersion until A releases.
	var updateErr error
	var updateWG sync.WaitGroup
	updateWG.Add(1)
	go func() {
		defer updateWG.Done()
		updateErr = m.Update(ctx, func(_ *chatstate.Tx) error {
			close(updateEntered)
			return nil
		})
	}()

	// Loop a few times re-checking that B is still blocked, with a
	// fresh round-trip through the database to give B's transaction
	// every chance to commit if the lock weren't held. The loop avoids
	// time.Sleep by using the database call itself as a "barrier".
	for range 5 {
		// Force a sync round-trip through the DB. This serves the
		// same role as a Sleep but is deterministic: by the time
		// this read completes, the scheduler has had a chance to
		// run goroutine B if it could make progress.
		_, err := f.DB.GetChatByID(ctx, created.Chat.ID)
		require.NoError(t, err)
		require.True(t, stillBlocked(updateEntered),
			"Update entered while Lock was still held")
	}

	// Release Lock and confirm Update completes successfully.
	close(releaseLock)
	require.True(t, waitForChan(ctx, updateEntered),
		"Update callback never started after Lock released")
	updateWG.Wait()
	lockWG.Wait()
	require.NoError(t, lockErr)
	require.NoError(t, updateErr)
}

// TestLockRollsBackCallbackError verifies that a Lock callback
// returning an error rolls back the surrounding transaction.
func TestLockRollsBackCallbackError(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := testutil.Context(t, testutil.WaitShort)
	created := createTestChat(t, f)
	m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})

	before := f.readChat(ctx, t, created.Chat.ID)
	publishedBefore := len(f.Pub.channels)

	sentinel := xerrors.New("lock callback error")
	err := m.Lock(ctx, func(store database.Store) error {
		// Try a write that should be rolled back.
		_, werr := store.UpdateChatByID(ctx, database.UpdateChatByIDParams{
			ID:    created.Chat.ID,
			Title: "rollback-me",
		})
		require.NoError(t, werr)
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	after := f.readChat(ctx, t, created.Chat.ID)
	require.Equal(t, before.Title, after.Title, "Lock callback error rolls back writes")
	require.Equal(t, publishedBefore, len(f.Pub.channels), "Lock publishes nothing on error")
}

// TestConcurrentUpdatesSerializeOnChatRow verifies that two
// goroutines racing to Update the same chat both succeed but their
// effects serialize on the chat row lock: snapshot_version advances
// by exactly N (one per Update) and each transition observes the
// effects of the prior one.
func TestConcurrentUpdatesSerializeOnChatRow(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := testutil.Context(t, testutil.WaitMedium)
	created := createTestChat(t, f)
	before := f.readChat(ctx, t, created.Chat.ID)

	const updates = 8
	var wg sync.WaitGroup
	wg.Add(updates)
	errs := make([]error, updates)
	for i := 0; i < updates; i++ {
		i := i
		go func() {
			defer wg.Done()
			m := chatstate.NewChatMachine(f.DB, f.Pub, created.Chat.ID, chatstate.Options{})
			errs[i] = m.Update(ctx, func(_ *chatstate.Tx) error { return nil })
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "concurrent update %d failed", i)
	}
	after := f.readChat(ctx, t, created.Chat.ID)
	require.Equal(t, before.SnapshotVersion+int64(updates), after.SnapshotVersion,
		"snapshot_version advanced by exactly one per update")
}

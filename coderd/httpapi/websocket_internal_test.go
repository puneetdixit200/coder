package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cdr.dev/slog/v3"
	"github.com/coder/coder/v2/testutil"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
)

// websocketPair sets up an httptest server with a websocket endpoint and
// returns the server-side conn. The server handler stays alive until ctx
// is done.
func websocketPair(ctx context.Context, t *testing.T) *websocket.Conn {
	t.Helper()
	serverConnCh := make(chan *websocket.Conn, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- conn
		// Keep the handler alive so the HTTP server doesn't close
		// the connection from under us.
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)

	//nolint:bodyclose
	clientConn, _, err := websocket.Dial(ctx, srv.URL, nil)
	require.NoError(t, err)
	_ = clientConn.CloseRead(ctx) // Needed to handle pings/pongs.
	t.Cleanup(func() {
		_ = clientConn.Close(websocket.StatusNormalClosure, "test cleanup")
	})

	select {
	case sc := <-serverConnCh:
		_ = sc.CloseRead(ctx) // Needed to handle pings/pongs.
		return sc
	case <-ctx.Done():
		t.Fatal("timed out waiting for server websocket accept")
		return nil
	}
}

func TestHeartbeatClose(t *testing.T) {
	t.Parallel()

	t.Run("Nilsafe", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(testutil.Context(t, testutil.WaitShort))
		sink := testutil.NewFakeSink(t)
		logger := sink.Logger()
		serverConn := websocketPair(ctx, t)

		var nilHbc *HeartbeatCloser

		deferCalled := make(chan struct{})
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("HeartbeatClose panicked: %v", r)
				}
				close(deferCalled)
			}()
			nilHbc.HeartbeatClose(ctx, logger, func() {}, serverConn)
		}()
		cancel()
		<-deferCalled
	})

	t.Run("ServerSideClose", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)

		sink := testutil.NewFakeSink(t)
		logger := sink.Logger()
		mClock := quartz.NewMock(t)
		hbCalls := atomic.Int64{}
		countFn := func(context.Context) {
			hbCalls.Add(1)
		}

		// Trap ticker creation so we can synchronize startup.
		trap := mClock.Trap().NewTicker("HeartbeatClose")
		defer trap.Close()

		serverConn := websocketPair(ctx, t)
		exitCalled := make(chan struct{})

		go heartbeatCloseWith(ctx, logger, countFn, func() {
			close(exitCalled)
		}, serverConn, mClock, time.Second)

		// Wait for the ticker to be created, then release.
		trap.MustWait(ctx).MustRelease(ctx)

		// Close the server-side connection before the tick fires.
		// The next ping will get net.ErrClosed.
		_ = serverConn.Close(websocket.StatusGoingAway, "simulated teardown")

		// Advance clock to trigger the tick.
		mClock.Advance(time.Second).MustWait(ctx)

		// Wait for heartbeatClose to call exit.
		select {
		case <-exitCalled:
		case <-ctx.Done():
			t.Fatal("timed out waiting for heartbeatClose to call exit")
		}

		// A closed connection is a normal shutdown condition. The
		// error should be logged at Debug, not Error.
		errorEntries := sink.Entries(func(e slog.SinkEntry) bool { return e.Level == slog.LevelError })
		assert.Empty(t, errorEntries,
			"closed connection should not produce error-level logs, got: %+v", errorEntries)
		debugEntries := sink.Entries(func(e slog.SinkEntry) bool { return e.Level == slog.LevelDebug })
		assert.NotEmpty(t, debugEntries,
			"expected a debug-level log entry for the closed connection")
		assert.Zero(t, hbCalls.Load(), "expected no heartbeat attempts")
	})

	t.Run("ContextCanceled", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)

		sink := testutil.NewFakeSink(t)
		logger := sink.Logger()
		mClock := quartz.NewMock(t)
		hbCalls := atomic.Int64{}
		countFn := func(context.Context) {
			hbCalls.Add(1)
		}

		trap := mClock.Trap().NewTicker("HeartbeatClose")
		defer trap.Close()

		serverCtx, serverCancel := context.WithCancel(ctx)
		serverConn := websocketPair(ctx, t)
		done := make(chan struct{})

		go func() {
			defer close(done)
			heartbeatCloseWith(serverCtx, logger, countFn, func() {
				t.Error("exit should not be called on context cancel")
			}, serverConn, mClock, time.Second)
		}()

		trap.MustWait(ctx).MustRelease(ctx)

		// Cancel the context. HeartbeatClose should return via
		// the <-ctx.Done() branch without calling exit.
		serverCancel()

		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("timed out waiting for heartbeatClose to return")
		}

		errorEntries := sink.Entries(func(e slog.SinkEntry) bool { return e.Level == slog.LevelError })
		assert.Empty(t, errorEntries,
			"context cancellation should not produce error-level logs, got: %+v", errorEntries)
		assert.Zero(t, hbCalls.Load(), "expected no successful heartbeats")
	})

	t.Run("PingSucceeds", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)

		sink := testutil.NewFakeSink(t)
		logger := sink.Logger()
		mClock := quartz.NewMock(t)
		hbCalls := atomic.Int64{}
		countFn := func(context.Context) {
			hbCalls.Add(1)
		}

		trap := mClock.Trap().NewTicker("HeartbeatClose")
		defer trap.Close()

		serverConn := websocketPair(ctx, t)
		exitCalled := make(chan struct{}, 1)

		go heartbeatCloseWith(ctx, logger, countFn, func() {
			exitCalled <- struct{}{}
		}, serverConn, mClock, time.Second)

		trap.MustWait(ctx).MustRelease(ctx)

		// Fire several ticks — pings should succeed each time.
		for i := range 3 {
			mClock.Advance(time.Second).MustWait(ctx)

			// Give the ping round-trip time to complete.
			// If exit were called, we'd catch it.
			testutil.Eventually(ctx, t, func(context.Context) bool {
				select {
				case <-exitCalled:
					t.Fatal("exit should not be called when pings succeed")
				default:
				}
				return hbCalls.Load() == int64(i+1)
			}, testutil.IntervalFast, "heartbeat counter not incremented at tick %d", i+1)
		}

		// No logs should be emitted during normal operation.
		errorEntries := sink.Entries(func(e slog.SinkEntry) bool { return e.Level == slog.LevelError })
		assert.Empty(t, errorEntries,
			"successful pings should not produce error-level logs, got: %+v", errorEntries)
		debugEntries := sink.Entries(func(e slog.SinkEntry) bool { return e.Level == slog.LevelDebug })
		assert.Empty(t, debugEntries,
			"successful pings should not produce debug-level logs, got: %+v", debugEntries)
		assert.Equal(t, 3, int(hbCalls.Load()), "expected heartbeat counter to be incremented by 3")
	})

	t.Run("RecordsPrometheusCounter", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)

		registry := prometheus.NewRegistry()
		heartbeatCloser := NewHeartbeatCloser().WithMetrics(func(context.Context) string {
			return "/test/path"
		})
		registry.MustRegister(heartbeatCloser)

		sink := testutil.NewFakeSink(t)
		logger := sink.Logger()
		mClock := quartz.NewMock(t)

		trap := mClock.Trap().NewTicker("HeartbeatClose")
		defer trap.Close()

		serverConn := websocketPair(ctx, t)
		exitCalled := make(chan struct{}, 1)

		go heartbeatCloseWith(ctx, logger, heartbeatCloser.recordHeartbeat, func() {
			exitCalled <- struct{}{}
		}, serverConn, mClock, time.Second)

		trap.MustWait(ctx).MustRelease(ctx)
		mClock.Advance(time.Second).MustWait(ctx)

		testutil.Eventually(ctx, t, func(context.Context) bool {
			select {
			case <-exitCalled:
				t.Fatal("exit should not be called when pings succeed")
			default:
			}
			metrics, err := registry.Gather()
			require.NoError(t, err)
			return testutil.PromCounterHasValue(t, metrics, 1,
				"coderd_api_websocket_heartbeats_total", "/test/path")
		}, testutil.IntervalFast, "heartbeat counter not incremented")
	})
}

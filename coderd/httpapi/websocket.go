package httpapi

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/xerrors"

	"cdr.dev/slog/v3"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
)

const HeartbeatInterval time.Duration = 15 * time.Second

const websocketHeartbeatsHelp = "Total successful WebSocket heartbeat " +
	"pings, labeled by route. Compare " +
	"rate(coderd_api_websocket_heartbeats_total[1m]) * 15 against " +
	"coderd_api_concurrent_websockets to detect zombie connections or " +
	"wedged handlers."

// HeartbeatCloser periodically checks websocket connection liveness and closes
// it if the ping fails. The zero value is safe for use.
type HeartbeatCloser struct {
	clk        quartz.Clock
	heartbeats *prometheus.CounterVec
	pathFn     func(context.Context) string
}

// NewHeartbeatCloser creates a new HeartbeatCloser without metrics.
func NewHeartbeatCloser() *HeartbeatCloser {
	hbc := &HeartbeatCloser{
		clk: quartz.NewReal(),
	}
	return hbc
}

// WithMetrics configures successful heartbeat counting. It must be called
// before the HeartbeatCloser is registered or used by any handlers. It is
// the responsibility of the caller to register HeartbeatCloser with a
// Prometheus registry.
func (hc *HeartbeatCloser) WithMetrics(pathFn func(context.Context) string) *HeartbeatCloser {
	hc.pathFn = pathFn
	hc.heartbeats = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "coderd",
		Subsystem: "api",
		Name:      "websocket_heartbeats_total",
		Help:      websocketHeartbeatsHelp,
	}, []string{"path"})
	if hc.clk == nil {
		hc.clk = quartz.NewReal()
	}
	return hc
}

func (hc *HeartbeatCloser) recordHeartbeat(ctx context.Context) {
	if hc == nil || hc.heartbeats == nil {
		return
	}
	path := "UNKNOWN"
	if hc.pathFn != nil {
		if rp := hc.pathFn(ctx); rp != "" {
			path = rp
		}
	}
	hc.heartbeats.WithLabelValues(path).Inc()
}

// HeartbeatClose loops to ping a WebSocket to keep it alive.
// It calls `exit` on ping failure.
func (hc *HeartbeatCloser) HeartbeatClose(ctx context.Context, logger slog.Logger, exit func(), conn *websocket.Conn) {
	if hc == nil || hc.clk == nil {
		heartbeatCloseWith(ctx, logger, nil, exit, conn, quartz.NewReal(), HeartbeatInterval)
		return
	}
	heartbeatCloseWith(ctx, logger, hc.recordHeartbeat, exit, conn, hc.clk, HeartbeatInterval)
}

// Collect implements prometheus.Collector.
func (hc *HeartbeatCloser) Collect(ch chan<- prometheus.Metric) {
	if hc == nil || hc.heartbeats == nil {
		return
	}
	hc.heartbeats.Collect(ch)
}

// Describe implements prometheus.Collector.
func (hc *HeartbeatCloser) Describe(ch chan<- *prometheus.Desc) {
	if hc == nil || hc.heartbeats == nil {
		return
	}
	hc.heartbeats.Describe(ch)
}

func heartbeatCloseWith(ctx context.Context, logger slog.Logger, recordHeartbeat func(context.Context), exit func(), conn *websocket.Conn, clk quartz.Clock, interval time.Duration) {
	ticker := clk.NewTicker(interval, "HeartbeatClose")
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := pingWithTimeout(ctx, conn, interval)
		if err != nil {
			// These errors are all expected during normal connection
			// teardown and should not be logged at error level:
			//   - context.DeadlineExceeded: client disconnected
			//     without sending a close frame.
			//   - context.Canceled: request context was canceled.
			//   - net.ErrClosed: connection was already closed by
			//     another goroutine (e.g. handler returned).
			//   - websocket.CloseError: a close frame was
			//     received or sent.
			if errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, context.Canceled) ||
				errors.Is(err, net.ErrClosed) ||
				websocket.CloseStatus(err) != -1 {
				logger.Debug(ctx, "heartbeat ping stopped", slog.Error(err))
			} else {
				logger.Error(ctx, "failed to heartbeat ping", slog.Error(err))
			}
			_ = conn.Close(websocket.StatusGoingAway, "Ping failed")
			exit()
			return
		}
		if recordHeartbeat != nil {
			recordHeartbeat(ctx)
		}
	}
}

func pingWithTimeout(ctx context.Context, conn *websocket.Conn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := conn.Ping(ctx)
	if err != nil {
		return xerrors.Errorf("failed to ping: %w", err)
	}

	return nil
}

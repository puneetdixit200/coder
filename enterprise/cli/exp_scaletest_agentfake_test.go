package cli_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"

	"cdr.dev/slog/v3"
	"cdr.dev/slog/v3/sloggers/slogtest"
	agplcli "github.com/coder/coder/v2/cli"
	"github.com/coder/coder/v2/testutil"
)

// Test_AgentFakeMetricsEndpoint asserts the wiring used by
// `coder exp scaletest agentfake` — agplcli.ServeHandler +
// promhttp.Handler() bound to the user-provided --prometheus-address —
// produces a scrapeable /metrics endpoint that includes Go-runtime
// metrics from the default Prometheus registry.
func Test_AgentFakeMetricsEndpoint(t *testing.T) {
	t.Parallel()

	// Reserve an ephemeral port. We close it before passing the addr to
	// ServeHandler; there's a tiny race window but it's acceptable for a
	// unit-level smoke test of the wiring.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(testutil.Context(t, testutil.WaitShort))
	defer cancel()

	logger := slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Leveled(slog.LevelDebug)
	closeSrv := agplcli.ServeHandler(ctx, logger, promhttp.Handler(), addr, "prometheus")
	defer closeSrv()

	// Wait until the listener is up, then scrape /metrics.
	url := "http://" + addr + "/metrics"
	var body []byte
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			return false
		}
		body, err = io.ReadAll(r.Body)
		return err == nil
	}, testutil.WaitShort, testutil.IntervalFast, "metrics endpoint never became reachable")

	// Default registry includes Go runtime + process collectors.
	require.Contains(t, string(body), "go_goroutines",
		"expected Go-runtime metric from default Prometheus registry")
}

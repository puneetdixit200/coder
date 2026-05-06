package httpapi

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type WebsocketMetrics struct {
	heartbeats *prometheus.CounterVec
	pathFn     func(context.Context) string
}

func (m *WebsocketMetrics) Heartbeat(ctx context.Context) {
	path := m.pathFn(ctx)
	m.heartbeats.WithLabelValues(path).Inc()
}

func NewWebsocketMetrics(r prometheus.Registerer, pathFromContext func(context.Context) string) *WebsocketMetrics {
	return &WebsocketMetrics{
		heartbeats: promauto.With(r).NewCounterVec(prometheus.CounterOpts{
			Namespace: "coderd",
			Subsystem: "api",
			Name:      "websocket_heartbeats_total",
			Help:      "Number of websocket heartbeats by path. Compare against coderd_api_concurrent_websockets_total to detect wedged handlers.",
		}, []string{"path"}),
		pathFn: pathFromContext,
	}
}

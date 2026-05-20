package agentfake

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds the Prometheus collectors exported by the agentfake
// manager and its agents. A nil *Metrics is a valid no-op; callers may
// pass nil to NewManager/NewAgent when metrics are not needed (for
// example, unit tests).
type Metrics struct {
	// ConnectedAgents is the number of fake agents whose dRPC connection
	// to a coderd replica is currently established. It increments after a
	// successful ConnectRPC and decrements when that connection ends
	// (either side closes, ctx cancels, or the stream errors out).
	ConnectedAgents prometheus.Gauge
}

// NewMetrics registers the agentfake collectors on reg and returns the
// Metrics handle. Pass prometheus.DefaultRegisterer to expose them on
// the default /metrics endpoint.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ConnectedAgents: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "coder",
			Subsystem: "scaletest_agentfake",
			Name:      "connected_agents",
			Help:      "Number of fake agents with an established dRPC connection to coderd.",
		}),
	}
	reg.MustRegister(m.ConnectedAgents)
	return m
}

// incConnected is a nil-safe Gauge.Inc.
func (m *Metrics) incConnected() {
	if m == nil {
		return
	}
	m.ConnectedAgents.Inc()
}

// decConnected is a nil-safe Gauge.Dec.
func (m *Metrics) decConnected() {
	if m == nil {
		return
	}
	m.ConnectedAgents.Dec()
}

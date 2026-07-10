package metrics

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var MatchTotalCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank", Subsystem: "matcher", Name: "match_total",
		Help: "Match outcomes grouped by match mode and status.",
	},
	[]string{"bucket", "status"},
)

var MatchTicketEventCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank", Subsystem: "matcher", Name: "ticket_events_total",
		Help: "Match ticket lifecycle events grouped by match mode and status.",
	},
	[]string{"match_mode", "status"},
)

var MatchLifecycleDurationHistogram = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "corerank", Subsystem: "matcher", Name: "lifecycle_duration_seconds",
		Help:    "Seconds from ticket creation to a terminal state.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	},
	[]string{"match_mode", "status"},
)

var QueuedTicketsGauge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "corerank", Subsystem: "matcher", Name: "queued_tickets",
		Help: "Current queued match tickets grouped by match mode.",
	},
	[]string{"match_mode"},
)

var RoomAssignmentCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank", Subsystem: "room", Name: "assignment_total",
		Help: "Room server assignment outcomes grouped by match mode and status.",
	},
	[]string{"match_mode", "status"},
)

var RoomAssignmentFailureCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank", Subsystem: "room", Name: "assignment_failures_total",
		Help: "Room server assignment failures grouped by match mode and reason.",
	},
	[]string{"match_mode", "reason"},
)

var RoomServerLoadGauge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "corerank", Subsystem: "room", Name: "server_load",
		Help: "Current reserved player slots on a room server.",
	},
	[]string{"server_id", "match_mode"},
)

var RequestTotalCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank", Subsystem: "grpc", Name: "requests_total",
		Help: "gRPC requests grouped by method and status.",
	},
	[]string{"method", "status"},
)

var RequestLatencyHistogram = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "corerank", Subsystem: "grpc", Name: "request_latency_seconds",
		Help: "gRPC request latency in seconds.",
		Buckets: []float64{
			0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
		},
	},
	[]string{"method"},
)

var ActivePlayersGauge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "corerank", Subsystem: "pool", Name: "active_players",
		Help: "Active players grouped by score bucket.",
	},
	[]string{"bucket"},
)

func RecordMatchSuccess(matchMode string) {
	MatchTotalCounter.WithLabelValues(normalizeLabel(matchMode), "success").Inc()
}

func RecordMatchTimeout(matchMode string) {
	MatchTotalCounter.WithLabelValues(normalizeLabel(matchMode), "timeout").Inc()
}

func RecordMatchCancelled(matchMode string) {
	MatchTotalCounter.WithLabelValues(normalizeLabel(matchMode), "cancelled").Inc()
}

func RecordMatchTicketEvents(matchMode, status string, count int) {
	if count > 0 {
		MatchTicketEventCounter.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(status)).Add(float64(count))
	}
}

func ObserveMatchLifecycle(matchMode, status string, createdAtMS, finishedAtMS int64) {
	if createdAtMS <= 0 || finishedAtMS < createdAtMS {
		return
	}
	seconds := float64(finishedAtMS-createdAtMS) / 1000
	MatchLifecycleDurationHistogram.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(status)).Observe(seconds)
}

func SetQueuedTickets(matchMode string, count int64) {
	if count < 0 {
		count = 0
	}
	QueuedTicketsGauge.WithLabelValues(normalizeLabel(matchMode)).Set(float64(count))
}

func RecordRoomAssignment(matchMode, status string) {
	RoomAssignmentCounter.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(status)).Inc()
}

func RecordRoomAssignmentFailure(matchMode, reason string) {
	RoomAssignmentFailureCounter.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(reason)).Inc()
}

func SetRoomServerLoad(serverID, matchMode string, load int64) {
	serverID = boundedLabel(serverID)
	if serverID == "unknown" {
		return
	}
	if load < 0 {
		load = 0
	}
	RoomServerLoadGauge.WithLabelValues(serverID, normalizeLabel(matchMode)).Set(float64(load))
}

func RecordRequest(method, status string) {
	RequestTotalCounter.WithLabelValues(boundedLabel(method), boundedLabel(status)).Inc()
}

func ObserveLatency(method string, seconds float64) {
	RequestLatencyHistogram.WithLabelValues(boundedLabel(method)).Observe(seconds)
}

func normalizeLabel(value string) string {
	return boundedLabel(strings.ToLower(strings.TrimSpace(value)))
}

func boundedLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) > 64 {
		return "other"
	}
	return value
}

package chronicle

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"sporemachine/internal/sporepb"
)

// Exporter holds last-value-per-node_id gauges for Grafana/Prometheus
// scraping. History/retention is not decided anywhere — this only exposes
// the latest snapshot per node.
type Exporter struct {
	registry *prometheus.Registry
	gauges   map[string]*prometheus.GaugeVec
}

func NewExporter() *Exporter {
	e := &Exporter{
		registry: prometheus.NewRegistry(),
		gauges:   make(map[string]*prometheus.GaugeVec),
	}
	for _, name := range []string{
		"cpu_percent", "memory_rss_bytes", "goroutines",
		"members_alive", "members_suspect", "members_dead",
		"commands_stored", "commands_succeeded", "commands_failed",
		"command_exec_ms_max", "command_exec_ms_min", "command_exec_ms_avg", "command_exec_ms_median",
		"storage_object_count", "storage_total_bytes", "storage_file_size_max", "storage_file_size_min",
		"merkle_sync_rounds", "merkle_sync_objects", "merkle_sync_bytes",
	} {
		gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "sporemachine",
			Name:      name,
		}, []string{"node_id"})
		e.registry.MustRegister(gv)
		e.gauges[name] = gv
	}
	return e
}

func (e *Exporter) Observe(s *sporepb.MetricsSnapshot) {
	id := s.GetNodeId()
	set := func(name string, v float64) { e.gauges[name].WithLabelValues(id).Set(v) }

	set("cpu_percent", s.GetCpuPercent())
	set("memory_rss_bytes", float64(s.GetMemoryRssBytes()))
	set("goroutines", float64(s.GetGoroutines()))
	set("members_alive", float64(s.GetMembersAlive()))
	set("members_suspect", float64(s.GetMembersSuspect()))
	set("members_dead", float64(s.GetMembersDead()))

	if c := s.GetCommands(); c != nil {
		set("commands_stored", float64(c.GetStored()))
		set("commands_succeeded", float64(c.GetSucceeded()))
		set("commands_failed", float64(c.GetFailed()))
		set("command_exec_ms_max", float64(c.GetExecMsMax()))
		set("command_exec_ms_min", float64(c.GetExecMsMin()))
		set("command_exec_ms_avg", c.GetExecMsAvg())
		set("command_exec_ms_median", float64(c.GetExecMsMedian()))
	}
	if st := s.GetStorage(); st != nil {
		set("storage_object_count", float64(st.GetObjectCount()))
		set("storage_total_bytes", float64(st.GetTotalBytes()))
		set("storage_file_size_max", float64(st.GetFileSizeMax()))
		set("storage_file_size_min", float64(st.GetFileSizeMin()))
	}
	if ms := s.GetMerkleSync(); ms != nil {
		set("merkle_sync_rounds", float64(ms.GetRounds()))
		set("merkle_sync_objects", float64(ms.GetObjectsSynced()))
		set("merkle_sync_bytes", float64(ms.GetBytesSynced()))
	}
}

func (e *Exporter) Handler() http.Handler {
	return promhttp.HandlerFor(e.registry, promhttp.HandlerOpts{})
}

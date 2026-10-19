package collector

import (
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// BaseLabels is the label set of every per-drive metric in the Python exporter.
var BaseLabels = []string{"drive", "type", "model_family", "model_name", "serial_number", "user_capacity"}

type family struct {
	help       string
	labelNames []string
}

type series struct {
	name        string
	labelValues []string
	value       float64
}

// Snapshot is an immutable set of gauge series produced by one collection cycle.
type Snapshot struct {
	families map[string]family
	series   []series
}

// Builder accumulates series for a Snapshot.
//
// Duplicate series (same name and label values) keep the last value, like
// Python's Gauge.set. A name keeps the HELP and label names it was first seen
// with; a series with a different label set is dropped, because Prometheus
// rejects mixed label sets within one metric family.
type Builder struct {
	log      *slog.Logger
	families map[string]family
	index    map[string]int
	series   []series
	warned   *sync.Map
}

// NewBuilder returns an empty Builder. warned de-duplicates label-mismatch
// warnings across cycles and may be shared.
func NewBuilder(log *slog.Logger, warned *sync.Map) *Builder {
	if warned == nil {
		warned = &sync.Map{}
	}
	return &Builder{log: log, families: map[string]family{}, index: map[string]int{}, warned: warned}
}

// Add records one gauge value.
func (b *Builder) Add(name, help string, labelNames, labelValues []string, value float64) {
	if fam, ok := b.families[name]; !ok {
		b.families[name] = family{help: help, labelNames: slices.Clone(labelNames)}
	} else if !slices.Equal(fam.labelNames, labelNames) {
		if _, seen := b.warned.LoadOrStore(name, true); !seen {
			b.log.Warn("metric label set changed, dropping series",
				"metric", name, "want", fam.labelNames, "got", labelNames)
		}
		return
	}
	key := name + "\xff" + strings.Join(labelValues, "\xff")
	if i, ok := b.index[key]; ok {
		b.series[i].value = value
		return
	}
	b.index[key] = len(b.series)
	b.series = append(b.series, series{name: name, labelValues: slices.Clone(labelValues), value: value})
}

// Snapshot freezes the builder. The builder must not be used afterwards.
func (b *Builder) Snapshot() *Snapshot {
	return &Snapshot{families: b.families, series: b.series}
}

// Len returns the number of series.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.series)
}

// SnapshotCollector exposes the latest Snapshot as a prometheus.Collector.
// It is "unchecked" (Describe sends nothing) because the metric set is only
// known after smartctl has run.
type SnapshotCollector struct {
	mu    sync.RWMutex
	snap  *Snapshot
	descs map[string]*prometheus.Desc
}

// Store replaces the exposed snapshot.
func (c *SnapshotCollector) Store(s *Snapshot) {
	descs := make(map[string]*prometheus.Desc, len(s.families))
	for name, f := range s.families {
		descs[name] = prometheus.NewDesc(name, f.help, f.labelNames, nil)
	}
	c.mu.Lock()
	c.snap, c.descs = s, descs
	c.mu.Unlock()
}

// Load returns the current snapshot (may be nil before the first cycle).
func (c *SnapshotCollector) Load() *Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

func (c *SnapshotCollector) Describe(chan<- *prometheus.Desc) {}

func (c *SnapshotCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	snap, descs := c.snap, c.descs
	c.mu.RUnlock()
	if snap == nil {
		return
	}
	for _, s := range snap.series {
		m, err := prometheus.NewConstMetric(descs[s.name], prometheus.GaugeValue, s.value, s.labelValues...)
		if err != nil {
			m = prometheus.NewInvalidMetric(descs[s.name], err)
		}
		ch <- m
	}
}

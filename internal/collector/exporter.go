package collector

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/l13t/smartctl_exporter/internal/smartctl"
)

// Config controls what the Exporter collects.
type Config struct {
	Discover       smartctl.DiscoverOptions
	RescanInterval time.Duration // 0: discover only once
	Concurrency    int
	StandbySkip    bool
	Extended       bool
	Flattener      Flattener
}

type point struct {
	name, help  string
	labelNames  []string
	labelValues []string
	value       float64
}

type deviceResult struct {
	points  []point
	standby bool
	err     error
}

// Exporter runs smartctl for every drive and publishes the results as a Snapshot.
type Exporter struct {
	cfg    Config
	runner smartctl.Runner
	log    *slog.Logger

	Snapshots *SnapshotCollector

	devices  []smartctl.Device
	lastScan time.Time
	cache    map[string][]point // last good points per drive, for standby
	warned   sync.Map

	duration    prometheus.Gauge
	lastSuccess prometheus.Gauge
	deviceCount prometheus.Gauge
	errors      *prometheus.CounterVec
}

// NewExporter registers the exporter's own metrics on reg.
func NewExporter(cfg Config, runner smartctl.Runner, log *slog.Logger, reg prometheus.Registerer) *Exporter {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	e := &Exporter{
		cfg:       cfg,
		runner:    runner,
		log:       log,
		Snapshots: &SnapshotCollector{},
		cache:     map[string][]point{},
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: Prefix + "exporter_collect_duration_seconds",
			Help: "Duration of the last collection cycle.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: Prefix + "exporter_last_collect_timestamp_seconds",
			Help: "Unix time of the last completed collection cycle.",
		}),
		deviceCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: Prefix + "exporter_devices",
			Help: "Number of devices being monitored.",
		}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: Prefix + "exporter_smartctl_errors_total",
			Help: "smartctl invocations that failed or returned unparsable output.",
		}, []string{"drive"}),
	}
	if reg != nil {
		reg.MustRegister(e.duration, e.lastSuccess, e.deviceCount, e.errors)
	}
	return e
}

// CollectOnce runs one cycle and stores a new snapshot.
func (e *Exporter) CollectOnce(ctx context.Context) error {
	start := time.Now()
	if err := e.maybeRescan(ctx, start); err != nil {
		return err
	}

	results := make([]deviceResult, len(e.devices))
	sem := make(chan struct{}, e.cfg.Concurrency)
	var wg sync.WaitGroup
	for i, dev := range e.devices {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			results[i] = e.collectDevice(ctx, dev)
		}()
	}
	wg.Wait()

	b := NewBuilder(e.log, &e.warned)
	cache := map[string][]point{}
	for i, dev := range e.devices {
		r := results[i]
		if r.err != nil {
			e.errors.WithLabelValues(dev.Name).Inc()
			e.log.Warn("collect failed", "drive", dev.Name, "err", r.err)
		}
		pts := r.points
		if r.standby {
			// Re-publish the last values so series don't vanish while the disk sleeps.
			pts = e.cache[dev.Name]
		}
		if len(pts) > 0 {
			cache[dev.Name] = pts
		}
		for _, p := range pts {
			b.Add(p.name, p.help, p.labelNames, p.labelValues, p.value)
		}
		if e.cfg.StandbySkip && dev.Protocol != smartctl.ProtocolNVMe {
			b.Add(Prefix+"device_in_standby", "1 if the drive was in standby and was not woken up.",
				BaseLabels, dev.LabelValues(), boolFloat(r.standby))
		}
	}
	e.cache = cache
	snap := b.Snapshot()
	e.Snapshots.Store(snap)

	e.deviceCount.Set(float64(len(e.devices)))
	e.duration.Set(time.Since(start).Seconds())
	e.lastSuccess.Set(float64(time.Now().Unix()))
	e.log.Debug("collection finished", "devices", len(e.devices), "series", snap.Len(), "took", time.Since(start))
	return nil
}

func (e *Exporter) maybeRescan(ctx context.Context, now time.Time) error {
	first := e.lastScan.IsZero()
	if !first && (e.cfg.RescanInterval <= 0 || now.Sub(e.lastScan) < e.cfg.RescanInterval) {
		return nil
	}
	devices, err := smartctl.Discover(ctx, e.runner, e.cfg.Discover, e.log)
	if err != nil {
		if first {
			return err
		}
		e.log.Warn("rescan failed, keeping previous device list", "err", err)
		return nil
	}
	e.devices, e.lastScan = devices, now
	return nil
}

func (e *Exporter) collectDevice(ctx context.Context, dev smartctl.Device) deviceResult {
	doc, exitCode, err := e.run(ctx, dev, "-A", "-H")
	if err != nil {
		return deviceResult{err: err}
	}
	if e.cfg.StandbySkip && isStandby(doc) {
		return deviceResult{standby: true}
	}

	labels := dev.LabelValues()
	var pts []point
	for _, a := range CompatAttrs(dev.Protocol, doc, exitCode) {
		pts = append(pts, point{CompatName(a.Key), CompatHelp(a.Key, a.Code, a.HasCode), BaseLabels, labels, a.Value})
	}
	pts = append(pts, infoPoint(dev))

	if e.cfg.Extended {
		ext, _, err := e.run(ctx, dev, "-x")
		if err != nil {
			// Compat metrics are still valid; report the error and carry on.
			return deviceResult{points: pts, err: err}
		}
		for _, s := range e.cfg.Flattener.Flatten(ext) {
			pts = append(pts, point{
				name:        ExtName(s.Path),
				help:        "smartctl JSON field " + joinPath(s.Path),
				labelNames:  append(slices.Clone(BaseLabels), s.LabelNames...),
				labelValues: append(slices.Clone(labels), s.LabelValues...),
				value:       s.Value,
			})
		}
	}
	return deviceResult{points: pts}
}

// run calls smartctl in the same argument order as the Python exporter:
// smartctl <mode...> -d <type> --json=c [-n standby] <device>
func (e *Exporter) run(ctx context.Context, dev smartctl.Device, mode ...string) (map[string]any, int, error) {
	args := append(slices.Clone(mode), "-d", dev.QueryType, "--json=c")
	if e.cfg.StandbySkip && dev.Protocol != smartctl.ProtocolNVMe {
		args = append(args, "-n", "standby")
	}
	res, err := e.runner.Run(ctx, append(args, dev.Path)...)
	if err != nil {
		return nil, 0, err
	}
	doc, err := res.JSON()
	return doc, res.ExitCode, err
}

func infoPoint(dev smartctl.Device) point {
	keys := sortedKeys(dev.Info)
	names := append(slices.Clone(BaseLabels), keys...)
	values := dev.LabelValues()
	for _, k := range keys {
		values = append(values, dev.Info[k])
	}
	return point{Prefix + "device_info", "Drive identity reported by smartctl -i; always 1.", names, values, 1}
}

// standbyMessage is printed (as JSON message) by smartctl -n when it skips a sleeping drive.
var standbyMessage = regexp.MustCompile(`^Device is in .* mode, exit\(\d+\)`)

func isStandby(doc map[string]any) bool {
	sc, _ := doc["smartctl"].(map[string]any)
	msgs, _ := sc["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if s, _ := mm["string"].(string); standbyMessage.MatchString(s) {
			return true
		}
	}
	return false
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Command smartctl_exporter exports smartctl data as Prometheus metrics, compatible
// with the metric names of matusnovak/prometheus-smartctl.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"

	"github.com/l13t/smartctl_exporter/internal/collector"
	"github.com/l13t/smartctl_exporter/internal/config"
	"github.com/l13t/smartctl_exporter/internal/smartctl"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	once := flag.Bool("once", false, "run one collection, print metrics to stdout and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*once); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(once bool) error {
	cfg, err := config.Load(os.Getenv, collector.DefaultExtendedExclude)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return fmt.Errorf("SMARTCTL_LOG_LEVEL: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        collector.Prefix + "exporter_build_info",
			Help:        "Exporter build information; always 1.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
	)

	runner := smartctl.ExecRunner{Path: cfg.SmartctlPath, Timeout: cfg.CommandTimeout, Logger: log}
	exp := collector.NewExporter(collector.Config{
		Discover: smartctl.DiscoverOptions{
			Include: cfg.Include,
			Exclude: cfg.Exclude,
			Manual:  smartctl.ParseManualDevices(cfg.Devices),
		},
		RescanInterval: cfg.RescanInterval,
		Concurrency:    cfg.Concurrency,
		StandbySkip:    cfg.StandbySkip,
		Extended:       cfg.Extended,
		Flattener:      collector.Flattener{Exclude: cfg.ExtendedExclude, MaxArray: cfg.ExtendedMaxArray},
	}, runner, log, reg)
	reg.MustRegister(exp.Snapshots)

	// The textfile only carries drive metrics: go_/process_ series would clash
	// with node_exporter's own.
	fileReg := prometheus.NewRegistry()
	fileReg.MustRegister(exp.Snapshots)

	if once {
		if err := exp.CollectOnce(ctx); err != nil {
			return err
		}
		return writeStdout(fileReg)
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Address, strconv.Itoa(cfg.Port)),
		Handler:           handler(reg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "url", "http://"+srv.Addr+"/metrics", "version", version)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ticker := time.NewTicker(cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		if err := exp.CollectOnce(ctx); err != nil {
			log.Error("collection failed", "err", err)
		} else if cfg.MetricsFile {
			path := filepath.Join(cfg.MetricsFilePath, "smartctl.prom")
			if err := prometheus.WriteToTextfile(path, fileReg); err != nil {
				log.Error("cannot write metrics file", "path", path, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		case err := <-errCh:
			return err
		case <-ticker.C:
		}
	}
}

func handler(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<html><body><h1>smartctl_exporter</h1><a href="/metrics">metrics</a></body></html>`))
	})
	return mux
}

func writeStdout(g prometheus.Gatherer) error {
	mfs, err := g.Gather()
	if err != nil {
		return err
	}
	enc := expfmt.NewEncoder(os.Stdout, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return err
		}
	}
	return nil
}

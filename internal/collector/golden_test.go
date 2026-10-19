package collector

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/l13t/smartctl_exporter/internal/smartctl"
)

var testdata = filepath.Join("..", "..", "testdata")

func newTestExporter(t *testing.T, cfg Config) (*Exporter, *prometheus.Registry) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(testdata, "scenario"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SMARTCTL_DIR", dir)
	runner := smartctl.ExecRunner{Path: filepath.Join(testdata, "fake-smartctl")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	e := NewExporter(cfg, runner, log, reg)
	reg.MustRegister(e.Snapshots)
	return e, reg
}

func collect(t *testing.T, e *Exporter, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	if err := e.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return seriesSet(mfs)
}

// seriesSet flattens metric families into "name{k="v",...}" -> value with sorted labels.
func seriesSet(mfs []*dto.MetricFamily) map[string]float64 {
	out := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var labels []string
			for _, lp := range m.GetLabel() {
				labels = append(labels, lp.GetName()+"="+`"`+lp.GetValue()+`"`)
			}
			slices.Sort(labels)
			out[mf.GetName()+"{"+strings.Join(labels, ",")+"}"] = m.GetGauge().GetValue()
		}
	}
	return out
}

func isCompat(key string) bool {
	for _, p := range []string{"smartprom_exporter_", "smartprom_ext_", "smartprom_device_info{", "smartprom_device_in_standby{"} {
		if strings.HasPrefix(key, p) {
			return false
		}
	}
	return strings.HasPrefix(key, Prefix)
}

// TestGoldenPythonCompat compares against testdata/golden/python.prom, produced
// by the original Python exporter on the same fixtures (scripts/gen_golden.py).
func TestGoldenPythonCompat(t *testing.T) {
	f, err := os.Open(filepath.Join(testdata, "golden", "python.prom"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	pyFamilies, err := parser.TextToMetricFamilies(f)
	if err != nil {
		t.Fatal(err)
	}
	var pyList []*dto.MetricFamily
	for _, mf := range pyFamilies {
		pyList = append(pyList, mf)
	}
	want := seriesSet(pyList)

	e, reg := newTestExporter(t, Config{Concurrency: 4})
	got := map[string]float64{}
	for k, v := range collect(t, e, reg) {
		if isCompat(k) {
			got[k] = v
		}
	}

	// Intentional differences, see README "Differences from the Python exporter".
	goOnly := map[string]bool{
		// Python dropped a drive entirely when ata_smart_attributes was missing.
		`smartprom_smart_passed{drive="/dev/sdg",model_family="Unknown",model_name="NOATTR-SSD",serial_number="NOATTR01",type="sat",user_capacity="256060514304"}`: true,
		`smartprom_exit_code{drive="/dev/sdg",model_family="Unknown",model_name="NOATTR-SSD",serial_number="NOATTR01",type="sat",user_capacity="256060514304"}`:    true,
	}

	for k, wv := range want {
		gv, ok := got[k]
		switch {
		case !ok:
			t.Errorf("missing series %s (python value %v)", k, wv)
		case gv != wv:
			t.Errorf("value mismatch %s: go=%v python=%v", k, gv, wv)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && !goOnly[k] {
			t.Errorf("unexpected series %s", k)
		}
	}
	for k := range goOnly {
		if _, ok := got[k]; !ok {
			t.Errorf("expected Go-only series %s", k)
		}
	}
	if t.Failed() {
		t.Logf("go=%d python=%d series", len(got), len(want))
	}
}

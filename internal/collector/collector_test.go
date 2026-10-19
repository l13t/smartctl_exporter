package collector

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/l13t/smartctl_exporter/internal/smartctl"
)

func TestParseRawString(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"33", 33, true},
		{"43 (Min/Max 39/46)", 43, true},
		{"20071h+27m+15.375s", 20071, true},
		{"1234 (0 2 0)", 1234, true},
		{"-5", -5, true},
		{"1_000", 1000, true},
		{"0/0", 0, false},
		{"0x0000", 0, false},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseRawString(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseRawString(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCompatName(t *testing.T) {
	cases := map[string]string{
		"Reallocated_Sector_Ct":   "smartprom_reallocated_sector_ct",
		"Power-Off_Retract_Count": "smartprom_power_off_retract_count",
		"Temperature_Celsius_raw": "smartprom_temperature_celsius_raw",
		"Total LBAs Written":      "smartprom_total_lbas_written",
		"Unc. Sector Ct":          "smartprom_unc_sector_ct",
		"Media/Data_Errors":       "smartprom_media_data_errors",
		"Read_Error_Rate (Ext)":   "smartprom_read_error_rate__ext_",
		"temperature_sensor1":     "smartprom_temperature_sensor1",
	}
	for in, want := range cases {
		if got := CompatName(in); got != want {
			t.Errorf("CompatName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CompatHelp("Power_On_Hours", 9, true); got != "(0x9) Power On Hours" {
		t.Errorf("CompatHelp = %q", got)
	}
}

func TestExtended(t *testing.T) {
	e, reg := newTestExporter(t, Config{
		Concurrency: 4,
		Extended:    true,
		Flattener:   Flattener{Exclude: regexp.MustCompile(DefaultExtendedExclude), MaxArray: 32},
	})
	got := collect(t, e, reg)

	sdf := `drive="/dev/sdf",model_family="Synthetic Edge Cases",model_name="EDGE-HDD-1",serial_number="EDGE0001",type="sat",user_capacity="1000204886016"`
	want := map[string]float64{
		key(`smartprom_ext_ata_device_statistics_pages_table_value`, sdf, `offset="16"`, `page="General Statistics"`, `page_number="1"`, `statistic="Power-on Hours"`):         20071,
		key(`smartprom_ext_ata_device_statistics_pages_table_value`, sdf, `offset="8"`, `page="Temperature Statistics"`, `page_number="5"`, `statistic="Current Temperature"`): 43,
		key(`smartprom_ext_ata_smart_attributes_table_raw_value`, sdf, `attribute_id="5"`, `attribute_name="Reallocated_Sector_Ct"`):                                           1234,
		key(`smartprom_ext_ata_smart_attributes_table_raw_value`, sdf, `attribute_id="203"`, `attribute_name="Unknown_Attribute"`):                                             2,
		key(`smartprom_ext_ata_smart_self_test_log_standard_table_status_passed`, sdf, `index="1"`):                                                                            0,
		key(`smartprom_ext_ata_smart_self_test_log_standard_table_lifetime_hours`, sdf, `index="0"`):                                                                           20070,
		key(`smartprom_ext_power_on_time_hours`, sdf):  20071,
		key(`smartprom_ext_smart_status_passed`, sdf):  0,
		key(`smartprom_ext_smartctl_exit_status`, sdf): 192,
	}
	for k, v := range want {
		if gv, ok := got[k]; !ok || gv != v {
			t.Errorf("%s = %v (present %v), want %v", k, gv, ok, v)
		}
	}
	for k := range got {
		for _, banned := range []string{"_json_format_version", "_table_flags_", "smartprom_ext_local_time", "smartprom_ext_smartctl_version", "smartprom_ext_wwn"} {
			if strings.Contains(k, banned) {
				t.Errorf("excluded path exported: %s", k)
			}
		}
	}

	// NVMe: new smartctl fields appear without code changes.
	var nvme []string
	for k := range got {
		if strings.HasPrefix(k, "smartprom_ext_nvme_smart_health_information_log_") && strings.Contains(k, `drive="/dev/nvme0"`) {
			nvme = append(nvme, k)
		}
	}
	if len(nvme) < 15 {
		t.Errorf("expected NVMe health log fields in extended output, got %d", len(nvme))
	}

	// Compat series are unaffected by extended mode.
	if v := got[`smartprom_power_on_hours_raw{`+sdf+`}`]; v != 20071 {
		t.Errorf("compat power_on_hours_raw = %v", v)
	}
}

func TestDeviceInfo(t *testing.T) {
	e, reg := newTestExporter(t, Config{Concurrency: 2})
	got := collect(t, e, reg)
	var found bool
	for k, v := range got {
		if strings.HasPrefix(k, "smartprom_device_info{") && strings.Contains(k, `drive="/dev/sdf"`) {
			found = true
			if v != 1 || !strings.Contains(k, `firmware_version="E1"`) || !strings.Contains(k, `smartctl_version="7.5"`) || !strings.Contains(k, `form_factor="3.5 inches"`) {
				t.Errorf("unexpected device_info %s = %v", k, v)
			}
		}
	}
	if !found {
		t.Error("smartprom_device_info for /dev/sdf missing")
	}
}

// awakeRunner hides "-n standby" so the fake smartctl reports the drive as active.
type awakeRunner struct {
	smartctl.Runner
	awake bool
}

func (r *awakeRunner) Run(ctx context.Context, args ...string) (smartctl.Result, error) {
	if r.awake {
		if i := slices.Index(args, "-n"); i >= 0 {
			args = slices.Delete(slices.Clone(args), i, i+2)
		}
	}
	return r.Runner.Run(ctx, args...)
}

func TestStandbySkip(t *testing.T) {
	e, reg := newTestExporter(t, Config{Concurrency: 4, StandbySkip: true})
	runner := &awakeRunner{Runner: e.runner, awake: true}
	e.runner = runner

	sde := `drive="/dev/sde",model_family="Western Digital Red",model_name="WDC WD20EFRX-68EUZN0",serial_number="REDACTED",type="sat",user_capacity="2000398934016"`
	first := collect(t, e, reg)
	if first[`smartprom_device_in_standby{`+sde+`}`] != 0 {
		t.Fatal("drive should be reported awake on first cycle")
	}
	power := first[`smartprom_power_on_hours_raw{`+sde+`}`]
	if power == 0 {
		t.Fatal("expected power_on_hours_raw on first cycle")
	}

	runner.awake = false
	second := collect(t, e, reg)
	if second[`smartprom_device_in_standby{`+sde+`}`] != 1 {
		t.Error("drive should be reported in standby on second cycle")
	}
	if second[`smartprom_power_on_hours_raw{`+sde+`}`] != power {
		t.Error("cached values should be re-exported while in standby")
	}
	// NVMe ignores -n and gets no standby metric.
	for k := range second {
		if strings.HasPrefix(k, "smartprom_device_in_standby{") && strings.Contains(k, `type="nvme"`) {
			t.Errorf("unexpected standby metric for NVMe: %s", k)
		}
	}
}

func TestBuilderDuplicatesAndLabelDrift(t *testing.T) {
	b := NewBuilder(discardLogger(), nil)
	b.Add("m", "h", []string{"a"}, []string{"1"}, 1)
	b.Add("m", "other help", []string{"a"}, []string{"1"}, 2)  // last wins, help kept
	b.Add("m", "h", []string{"a", "b"}, []string{"1", "2"}, 3) // label drift dropped
	s := b.Snapshot()
	if s.Len() != 1 || s.series[0].value != 2 || s.families["m"].help != "h" {
		t.Errorf("unexpected snapshot: %+v", s)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// key builds a seriesSet key; each part is one or more comma-separated k="v" pairs.
func key(name string, parts ...string) string {
	var labels []string
	for _, p := range parts {
		labels = append(labels, strings.Split(p, ",")...)
	}
	slices.Sort(labels)
	return name + "{" + strings.Join(labels, ",") + "}"
}

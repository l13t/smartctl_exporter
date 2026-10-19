package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaultsMatchPython(t *testing.T) {
	c, err := Load(env(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "0.0.0.0" || c.Port != 9902 || c.RefreshInterval != 60*time.Second ||
		c.MetricsFile || c.MetricsFilePath != "/metrics/" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestParse(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SMARTCTL_REFRESH_INTERVAL":    "300",
		"SMARTCTL_RESCAN_INTERVAL":     "1h",
		"SMARTCTL_METRICS_FILE_ENABLE": "True",
		"SMARTCTL_DEVICE_EXCLUDE":      "^/dev/sd[xy]$",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshInterval != 5*time.Minute || c.RescanInterval != time.Hour || !c.MetricsFile ||
		!c.Exclude.MatchString("/dev/sdx") {
		t.Errorf("unexpected config: %+v", c)
	}

	if _, err := Load(env(map[string]string{"SMARTCTL_EXPORTER_PORT": "x", "SMARTCTL_DEVICE_INCLUDE": "("}), ""); err == nil {
		t.Error("expected error for invalid values")
	}
}

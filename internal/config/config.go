// Package config reads exporter settings from the environment. Variable names
// and defaults match the Python prometheus-smartctl exporter where they exist.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address         string
	Port            int
	RefreshInterval time.Duration
	MetricsFile     bool
	MetricsFilePath string

	SmartctlPath   string
	CommandTimeout time.Duration
	RescanInterval time.Duration
	Concurrency    int
	Devices        string
	Include        *regexp.Regexp
	Exclude        *regexp.Regexp
	StandbySkip    bool

	Extended         bool
	ExtendedExclude  *regexp.Regexp
	ExtendedMaxArray int

	LogLevel string
}

// Load parses the environment. getenv is os.Getenv in production.
func Load(getenv func(string) string, defaultExtendedExclude string) (Config, error) {
	p := parser{getenv: getenv}
	c := Config{
		Address:         p.str("SMARTCTL_EXPORTER_ADDRESS", "0.0.0.0"),
		Port:            p.int("SMARTCTL_EXPORTER_PORT", 9902),
		RefreshInterval: p.seconds("SMARTCTL_REFRESH_INTERVAL", 60),
		MetricsFile:     p.bool("SMARTCTL_METRICS_FILE_ENABLE", false),
		MetricsFilePath: p.str("SMARTCTL_METRICS_FILE_PATH", "/metrics/"),

		SmartctlPath:   p.str("SMARTCTL_PATH", "smartctl"),
		CommandTimeout: p.seconds("SMARTCTL_COMMAND_TIMEOUT", 30),
		RescanInterval: p.seconds("SMARTCTL_RESCAN_INTERVAL", 600),
		Concurrency:    p.int("SMARTCTL_CONCURRENCY", 4),
		Devices:        p.str("SMARTCTL_DEVICES", ""),
		Include:        p.regexp("SMARTCTL_DEVICE_INCLUDE", ""),
		Exclude:        p.regexp("SMARTCTL_DEVICE_EXCLUDE", ""),
		StandbySkip:    p.bool("SMARTCTL_STANDBY_SKIP", false),

		Extended:         p.bool("SMARTCTL_EXTENDED", false),
		ExtendedExclude:  p.regexp("SMARTCTL_EXTENDED_EXCLUDE", defaultExtendedExclude),
		ExtendedMaxArray: p.int("SMARTCTL_EXTENDED_MAX_ARRAY", 32),

		LogLevel: p.str("SMARTCTL_LOG_LEVEL", "info"),
	}
	if c.RefreshInterval <= 0 {
		p.errs = append(p.errs, errors.New("SMARTCTL_REFRESH_INTERVAL must be > 0"))
	}
	return c, errors.Join(p.errs...)
}

type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

func (p *parser) int(key string, def int) int {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", key, err))
		return def
	}
	return n
}

// seconds accepts a plain number of seconds (Python format) or a Go duration.
func (p *parser) seconds(key string, def int) time.Duration {
	v := p.str(key, "")
	if v == "" {
		return time.Duration(def) * time.Second
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: want seconds or duration: %w", key, err))
		return time.Duration(def) * time.Second
	}
	return d
}

func (p *parser) bool(key string, def bool) bool {
	switch strings.ToLower(p.str(key, "")) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	p.errs = append(p.errs, fmt.Errorf("%s: want true/false", key))
	return def
}

func (p *parser) regexp(key, def string) *regexp.Regexp {
	v := p.str(key, def)
	if v == "" {
		return nil
	}
	re, err := regexp.Compile(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", key, err))
		return nil
	}
	return re
}

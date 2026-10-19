package collector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/l13t/smartctl_exporter/internal/smartctl"
)

// Attr is one value in the Python exporter's attribute dict.
type Attr struct {
	Key     string
	Value   float64
	Code    int  // ATA attribute id, used only for HELP
	HasCode bool // true for SAT attributes (Python printed hex(id))
}

// CompatAttrs converts `smartctl -A -H --json` output into the attribute list
// the Python exporter produced. Order is preserved; later duplicates win when
// they are turned into series, as with Python's Gauge.set.
//
// Unlike Python, a missing attribute section does not drop the whole drive:
// smart_passed and exit_code are still reported.
func CompatAttrs(p smartctl.Protocol, results map[string]any, exitCode int) []Attr {
	var attrs []Attr
	var sat bool
	switch p {
	case smartctl.ProtocolATA:
		attrs, sat = satAttrs(results), true
	case smartctl.ProtocolNVMe:
		attrs = nvmeAttrs(results)
	case smartctl.ProtocolSCSI:
		attrs = scsiAttrs(results)
	}
	return append(attrs,
		Attr{Key: "smart_passed", Value: smartStatus(results), HasCode: sat},
		Attr{Key: "exit_code", Value: float64(exitCode), HasCode: sat},
	)
}

// smartStatus returns 1, 0 or -1 for passed, failed or unknown.
func smartStatus(results map[string]any) float64 {
	status, ok := results["smart_status"].(map[string]any)
	if !ok {
		return -1
	}
	if passed, _ := status["passed"].(bool); passed {
		return 1
	}
	return 0
}

func satAttrs(results map[string]any) []Attr {
	section, _ := results["ata_smart_attributes"].(map[string]any)
	table, _ := section["table"].([]any)
	var attrs []Attr
	for _, row := range table {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		code, _ := toInt(m["id"])
		value, ok := toFloat(m["value"])
		if name == "" || !ok {
			continue
		}
		attrs = append(attrs, Attr{Key: name, Value: value, Code: code, HasCode: true})
		rawString := ""
		if raw, ok := m["raw"].(map[string]any); ok {
			rawString, _ = raw["string"].(string)
		}
		if v, ok := ParseRawString(rawString); ok {
			attrs = append(attrs, Attr{Key: name + "_raw", Value: v, Code: code, HasCode: true})
		}
	}
	return attrs
}

var pyInt = regexp.MustCompile(`^[+-]?[0-9]+(_[0-9]+)*$`)

// ParseRawString mirrors the Python logic for raw.string:
//
//	int(s.split()[0])            -> "33", "43 (Min/Max 39/46)"
//	int(s.split("h+")[0])        -> "20071h+27m+15.375s"
//
// Anything else (e.g. "0/0", "0x0000") yields no _raw metric.
func ParseRawString(s string) (float64, bool) {
	if fields := strings.Fields(s); len(fields) > 0 {
		if v, ok := parsePyInt(fields[0]); ok {
			return v, true
		}
	}
	if strings.Contains(s, "h+") {
		head, _, _ := strings.Cut(s, "h+")
		return parsePyInt(strings.TrimSpace(head))
	}
	return 0, false
}

func parsePyInt(s string) (float64, bool) {
	if !pyInt.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	return v, err == nil
}

func nvmeAttrs(results map[string]any) []Attr {
	log, _ := results["nvme_smart_health_information_log"].(map[string]any)
	var attrs []Attr
	for _, key := range sortedKeys(log) {
		value := log[key]
		if key == "temperature_sensors" {
			sensors, _ := value.([]any)
			for i, s := range sensors {
				if v, ok := toFloat(s); ok {
					attrs = append(attrs, Attr{Key: fmt.Sprintf("temperature_sensor%d", i+1), Value: v})
				}
			}
			continue
		}
		// Python called Gauge.set() on every value; non-numeric ones raised.
		if v, ok := toFloat(value); ok {
			attrs = append(attrs, Attr{Key: key, Value: v})
		}
	}
	return attrs
}

func scsiAttrs(results map[string]any) []Attr {
	var attrs []Attr
	for _, key := range sortedKeys(results) {
		switch v := results[key].(type) {
		case map[string]any:
			for _, sub := range sortedKeys(v) {
				if f, ok := pyIntValue(v[sub]); ok {
					attrs = append(attrs, Attr{Key: key + "_" + sub, Value: f})
				}
			}
		default:
			if f, ok := pyIntValue(v); ok {
				attrs = append(attrs, Attr{Key: key, Value: f})
			}
		}
	}
	return attrs
}

// pyIntValue matches Python's `type(v) == int`: integral JSON numbers only,
// no floats and no booleans.
func pyIntValue(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(n.String(), ".eE") {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n.String())
	return i, err == nil
}

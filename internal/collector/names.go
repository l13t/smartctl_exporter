package collector

import (
	"fmt"
	"strings"
)

const (
	// Prefix is shared by every metric, as in the Python exporter.
	Prefix = "smartprom_"
	// ExtPrefix marks metrics produced by the generic extended flattener.
	ExtPrefix = Prefix + "ext_"
)

var pythonReplacer = strings.NewReplacer("-", "_", " ", "_", ".", "", "/", "_")

// CompatName reproduces the Python exporter's metric naming:
//
//	"smartprom_" + key.replace("-","_").replace(" ","_").replace(".","").replace("/","_").lower()
//
// Any character still invalid in a Prometheus metric name is replaced by "_".
// Python would have failed to register such a metric, so this never renames a
// series that used to exist.
func CompatName(key string) string {
	return sanitize(Prefix + strings.ToLower(pythonReplacer.Replace(key)))
}

// ExtName builds the metric name for a flattened JSON path.
func ExtName(path []string) string {
	return sanitize(ExtPrefix + strings.ToLower(strings.Join(path, "_")))
}

// CompatHelp mirrors the Python HELP string "(0x<id>) <key with spaces>".
func CompatHelp(key string, code int, hasCode bool) string {
	desc := strings.ReplaceAll(key, "_", " ")
	if hasCode {
		return fmt.Sprintf("(%#x) %s", code, desc)
	}
	return desc
}

func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == ':':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sanitizeLabel makes s a valid label name (no colons allowed).
func sanitizeLabel(s string) string {
	return strings.ReplaceAll(sanitize(strings.ToLower(s)), ":", "_")
}

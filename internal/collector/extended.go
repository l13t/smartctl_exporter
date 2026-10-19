package collector

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
)

// DefaultExtendedExclude drops paths that are identity, build metadata or
// per-call noise rather than drive health. Matched against the flattened path
// without prefix, e.g. "smartctl_svn_revision".
const DefaultExtendedExclude = `^(json_format_version|local_time|wwn|nvme_pci_vendor|nvme_ieee_oui_identifier|smartctl_(version|svn_revision|uint128_precision_bits|pre_release)|(ata_smart_attributes|ata_device_statistics_pages)_table_flags)(_|$)`

// ExtSample is one flattened numeric leaf.
type ExtSample struct {
	Path        []string
	LabelNames  []string
	LabelValues []string
	Value       float64
}

// tableSpec gives array elements meaningful labels instead of an index.
type tableSpec struct {
	labels map[string]string // element key -> label name
}

// knownTables are matched on the array's flattened path.
var knownTables = map[string]tableSpec{
	"ata_smart_attributes_table":        {labels: map[string]string{"id": "attribute_id", "name": "attribute_name"}},
	"ata_device_statistics_pages":       {labels: map[string]string{"number": "page_number", "name": "page"}},
	"ata_device_statistics_pages_table": {labels: map[string]string{"offset": "offset", "name": "statistic"}},
	"sas_phy_event_counters":            {labels: map[string]string{"id": "event_id", "name": "event"}},
}

// Flattener turns arbitrary smartctl JSON into samples, so fields added by
// future smartctl versions show up without code changes.
type Flattener struct {
	Exclude  *regexp.Regexp
	MaxArray int
}

type label struct{ name, value string }

// Flatten walks the document. Numbers and booleans become samples; strings are
// ignored (identity strings are exported via smartprom_device_info instead).
func (f Flattener) Flatten(doc map[string]any) []ExtSample {
	var out []ExtSample
	f.walk(nil, doc, nil, &out)
	return out
}

func (f Flattener) walk(path []string, v any, labels []label, out *[]ExtSample) {
	if len(path) > 0 && f.Exclude != nil && f.Exclude.MatchString(joinPath(path)) {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			f.walk(appendPath(path, k), t[k], labels, out)
		}
	case []any:
		f.walkArray(path, t, labels, out)
	default:
		val, ok := toFloat(v)
		if !ok || len(path) == 0 {
			return
		}
		s := ExtSample{Path: slices.Clone(path), Value: val}
		for _, l := range labels {
			s.LabelNames = append(s.LabelNames, l.name)
			s.LabelValues = append(s.LabelValues, l.value)
		}
		*out = append(*out, s)
	}
}

func (f Flattener) walkArray(path []string, arr []any, labels []label, out *[]ExtSample) {
	limit := len(arr)
	if f.MaxArray > 0 && limit > f.MaxArray {
		limit = f.MaxArray
	}
	spec, known := knownTables[joinPath(path)]
	for i, el := range arr[:limit] {
		elLabels := slices.Clone(labels)
		obj, isObj := el.(map[string]any)
		used := map[string]bool{}
		switch {
		case known && isObj:
			for _, key := range sortedKeys(spec.labels) {
				if s := labelValue(obj[key]); s != "" {
					elLabels = addLabel(elLabels, spec.labels[key], s)
					used[key] = true
				}
			}
		case isObj && labelValue(obj["name"]) != "":
			elLabels = addLabel(elLabels, "name", labelValue(obj["name"]))
			used["name"] = true
		}
		if len(elLabels) == len(labels) {
			elLabels = addLabel(elLabels, "index", strconv.Itoa(i))
		}
		if isObj {
			for _, k := range sortedKeys(obj) {
				if used[k] {
					continue
				}
				f.walk(appendPath(path, k), obj[k], elLabels, out)
			}
			continue
		}
		f.walk(path, el, elLabels, out)
	}
}

// addLabel appends a label, suffixing the name if a parent array used it.
func addLabel(labels []label, name, value string) []label {
	name = sanitizeLabel(name)
	base := name
	for n := 2; slices.ContainsFunc(labels, func(l label) bool { return l.name == name }) || isBaseLabel(name); n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	return append(labels, label{name, value})
}

func isBaseLabel(name string) bool { return slices.Contains(BaseLabels, name) }

func labelValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := pyIntValue(v); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

func appendPath(path []string, k string) []string {
	return append(slices.Clip(path), k)
}

func joinPath(path []string) string {
	n := 0
	for _, p := range path {
		n += len(p) + 1
	}
	b := make([]byte, 0, n)
	for i, p := range path {
		if i > 0 {
			b = append(b, '_')
		}
		b = append(b, p...)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

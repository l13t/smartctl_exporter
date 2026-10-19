package smartctl

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// Protocol selects which parser handles a device's attributes.
type Protocol string

const (
	ProtocolATA     Protocol = "ATA"
	ProtocolNVMe    Protocol = "NVMe"
	ProtocolSCSI    Protocol = "SCSI"
	ProtocolUnknown Protocol = ""
)

// Device type groups known to the original Python exporter.
// https://www.smartmontools.org/wiki/USB
var (
	satTypes  = []string{"sat", "usbjmicron", "usbprolific", "usbsunplus"}
	nvmeTypes = []string{"nvme", "sntasmedia", "sntjmicron", "sntrealtek"}
	scsiTypes = []string{"scsi"}
)

// megaraidType matches scan types such as "megaraid,0" or "sat+megaraid,0".
var megaraidType = regexp.MustCompile(`(sat\+)?(megaraid,\d+)`)

// megaraidOpenError is reported by --scan-open for MegaRAID virtual drives.
const megaraidOpenError = "DELL or MegaRaid controller, please try adding '-d megaraid,N'"

// Device is a discovered drive plus the identity labels attached to its metrics.
type Device struct {
	// Name is the "drive" label: the device path, or "megaraid,N" for drives
	// behind a MegaRAID controller (matching the Python exporter).
	Name string
	// Path is the device argument passed to smartctl (/dev/sda, /dev/bus/0).
	Path string
	// Type is the "type" label. For MegaRAID drives it is "sat" or "scsi".
	Type string
	// QueryType is the value passed to smartctl -d.
	QueryType string
	Protocol  Protocol
	Megaraid  bool

	ModelFamily  string
	ModelName    string
	SerialNumber string
	UserCapacity string

	// Info holds additional identity strings exposed via smartprom_device_info.
	Info map[string]string
}

// LabelValues returns values for the base label set, in collector order.
func (d Device) LabelValues() []string {
	return []string{d.Name, d.Type, d.ModelFamily, d.ModelName, d.SerialNumber, d.UserCapacity}
}

// ManualDevice is a user-provided device, bypassing --scan-open.
type ManualDevice struct {
	Path string
	Type string // empty means "let smartctl autodetect"
}

// ParseManualDevices parses "PATH[:TYPE] PATH[:TYPE] ..." (whitespace separated,
// because MegaRAID types contain commas).
func ParseManualDevices(s string) []ManualDevice {
	var out []ManualDevice
	for f := range strings.FieldsSeq(s) {
		path, typ, _ := strings.Cut(f, ":")
		out = append(out, ManualDevice{Path: path, Type: typ})
	}
	return out
}

// DiscoverOptions controls device discovery.
type DiscoverOptions struct {
	Include *regexp.Regexp
	Exclude *regexp.Regexp
	Manual  []ManualDevice
}

type scanEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	OpenError string `json:"open_error"`
}

// Discover lists devices and queries their identity once.
func Discover(ctx context.Context, r Runner, opts DiscoverOptions, log *slog.Logger) ([]Device, error) {
	entries, err := scan(ctx, r, opts.Manual)
	if err != nil {
		return nil, err
	}

	var devices []Device
	seen := map[string]bool{}
	for _, e := range entries {
		if e.OpenError == megaraidOpenError {
			continue
		}
		dev, err := identify(ctx, r, e)
		if err != nil {
			log.Warn("cannot identify device", "device", e.Name, "type", e.Type, "err", err)
			continue
		}
		if dev.Protocol == ProtocolUnknown {
			log.Warn("unsupported device type, skipping", "device", e.Name, "type", e.Type)
			continue
		}
		if seen[dev.Name] {
			// The Python exporter keyed drives by "megaraid,N" alone, so the same
			// slot on a second controller silently replaced the first one.
			renamed := e.Name + ":" + dev.Name
			log.Warn("duplicate drive name, disambiguating", "drive", dev.Name, "renamed", renamed)
			dev.Name = renamed
		}
		if !keep(dev.Name, opts) {
			log.Debug("device filtered out", "drive", dev.Name)
			continue
		}
		seen[dev.Name] = true
		log.Info("discovered device", "drive", dev.Name, "type", dev.Type, "protocol", dev.Protocol,
			"model_family", dev.ModelFamily, "model_name", dev.ModelName,
			"serial_number", dev.SerialNumber, "user_capacity", dev.UserCapacity)
		devices = append(devices, dev)
	}
	if len(devices) == 0 {
		log.Warn("no devices found, make sure you have enough privileges")
	}
	return devices, nil
}

func keep(name string, opts DiscoverOptions) bool {
	if opts.Include != nil && !opts.Include.MatchString(name) {
		return false
	}
	if opts.Exclude != nil && opts.Exclude.MatchString(name) {
		return false
	}
	return true
}

func scan(ctx context.Context, r Runner, manual []ManualDevice) ([]scanEntry, error) {
	if len(manual) > 0 {
		entries := make([]scanEntry, 0, len(manual))
		for _, m := range manual {
			entries = append(entries, scanEntry{Name: m.Path, Type: m.Type})
		}
		return entries, nil
	}
	res, err := r.Run(ctx, "--scan-open", "--json=c")
	if err != nil {
		return nil, err
	}
	var out struct {
		Devices []scanEntry `json:"devices"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("parse --scan-open output: %w", err)
	}
	return out.Devices, nil
}

func identify(ctx context.Context, r Runner, e scanEntry) (Device, error) {
	args := []string{"-i", "--json=c"}
	if e.Type != "" {
		args = append(args, "-d", e.Type)
	}
	res, err := r.Run(ctx, append(args, e.Name)...)
	if err != nil {
		return Device{}, err
	}
	info, err := res.JSON()
	if err != nil {
		return Device{}, err
	}

	devType := e.Type
	if devType == "" {
		devType = str(dig(info, "device", "type"), "unknown")
	}
	dev := Device{
		Name:         e.Name,
		Path:         e.Name,
		Type:         devType,
		QueryType:    devType,
		ModelFamily:  str(info["model_family"], "Unknown"),
		ModelName:    str(info["model_name"], "Unknown"),
		SerialNumber: str(info["serial_number"], "Unknown"),
		UserCapacity: str(dig(info, "user_capacity", "bytes"), "Unknown"),
		Info:         infoLabels(info),
	}
	protocol := Protocol(str(dig(info, "device", "protocol"), ""))

	if m := megaraidType.FindStringSubmatch(devType); m != nil {
		dev.Megaraid = true
		dev.Name = m[2]
		// smartmontools r5286+ reports SAS model names as scsi_model_name.
		dev.ModelName = str(info["scsi_model_name"], dev.ModelName)
		switch protocol {
		case ProtocolATA:
			dev.Type = "sat"
		case "":
			dev.Type = "unknown"
		default:
			dev.Type = "scsi"
		}
	}

	dev.Protocol = classify(devType, protocol)
	return dev, nil
}

// classify keeps the Python exporter's type groups and additionally accepts any
// device type whose reported protocol is one we can parse (e.g. "ata", "atacam",
// "cciss,N", "areca,N"), which the Python exporter silently skipped.
func classify(typ string, protocol Protocol) Protocol {
	switch {
	case slices.Contains(satTypes, typ):
		return ProtocolATA
	case slices.Contains(nvmeTypes, typ):
		return ProtocolNVMe
	case slices.Contains(scsiTypes, typ):
		return ProtocolSCSI
	}
	switch protocol {
	case ProtocolATA, ProtocolNVMe, ProtocolSCSI:
		return protocol
	}
	return ProtocolUnknown
}

func infoLabels(info map[string]any) map[string]string {
	out := map[string]string{
		"protocol":         str(dig(info, "device", "protocol"), ""),
		"firmware_version": str(info["firmware_version"], str(info["scsi_revision"], "")),
		"form_factor":      str(dig(info, "form_factor", "name"), ""),
		"rotation_rate":    str(info["rotation_rate"], ""),
		"interface_speed":  str(dig(info, "interface_speed", "current", "string"), ""),
		"wwn":              wwn(info),
		"scsi_model_name":  str(info["scsi_model_name"], ""),
		"scsi_vendor":      str(info["scsi_vendor"], ""),
		"smartctl_version": "",
	}
	if v, ok := dig(info, "smartctl", "version").([]any); ok {
		parts := make([]string, 0, len(v))
		for _, p := range v {
			parts = append(parts, str(p, ""))
		}
		out["smartctl_version"] = strings.Join(parts, ".")
	}
	return out
}

func wwn(info map[string]any) string {
	w, ok := info["wwn"].(map[string]any)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s-%s-%s", str(w["naa"], ""), str(w["oui"], ""), str(w["id"], ""))
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func str(v any, def string) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "True"
		}
		return "False"
	}
	return def
}

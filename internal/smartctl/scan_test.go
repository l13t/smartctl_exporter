package smartctl

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mapRunner answers by joined args.
type mapRunner map[string]string

func (m mapRunner) Run(_ context.Context, args ...string) (Result, error) {
	key := strings.Join(args, " ")
	out, ok := m[key]
	if !ok {
		return Result{}, fmt.Errorf("unexpected call: %s", key)
	}
	return Result{Args: args, Stdout: []byte(out)}, nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func names(devs []Device) []string {
	var out []string
	for _, d := range devs {
		out = append(out, d.Name+"|"+d.Type+"|"+d.QueryType+"|"+string(d.Protocol))
	}
	return out
}

func TestDiscoverMegaraidCollisionAndFilters(t *testing.T) {
	sas := `{"device":{"protocol":"SCSI"},"scsi_model_name":"SAS DISK","serial_number":"S1","user_capacity":{"bytes":100}}`
	ata := `{"device":{"protocol":"ATA"},"model_name":"ATA DISK","serial_number":"A1"}`
	r := mapRunner{
		"--scan-open --json=c": `{"devices":[
			{"name":"/dev/bus/0","type":"megaraid,0"},
			{"name":"/dev/bus/1","type":"sat+megaraid,0"},
			{"name":"/dev/sdx","type":"scsi","open_error":"DELL or MegaRaid controller, please try adding '-d megaraid,N'"},
			{"name":"/dev/sda","type":"ata"},
			{"name":"/dev/sdz","type":"weird"}]}`,
		"-i --json=c -d megaraid,0 /dev/bus/0":     sas,
		"-i --json=c -d sat+megaraid,0 /dev/bus/1": ata,
		"-i --json=c -d ata /dev/sda":              ata,
		"-i --json=c -d weird /dev/sdz":            `{"device":{"protocol":"Other"}}`,
	}
	devs, err := Discover(context.Background(), r, DiscoverOptions{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"megaraid,0|scsi|megaraid,0|SCSI",
		"/dev/bus/1:megaraid,0|sat|sat+megaraid,0|ATA",
		"/dev/sda|ata|ata|ATA", // Python skipped "ata"; supported via protocol now
	}
	if got := names(devs); !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if devs[0].ModelName != "SAS DISK" || devs[0].UserCapacity != "100" || devs[0].ModelFamily != "Unknown" {
		t.Errorf("megaraid identity: %+v", devs[0])
	}

	devs, err = Discover(context.Background(), r, DiscoverOptions{
		Include: regexp.MustCompile(`megaraid`),
		Exclude: regexp.MustCompile(`^/dev/bus/1`),
	}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(devs); !slices.Equal(got, []string{"megaraid,0|scsi|megaraid,0|SCSI"}) {
		t.Errorf("filtered: %v", got)
	}
}

func TestManualDevices(t *testing.T) {
	got := ParseManualDevices("/dev/sda:sat  /dev/bus/0:sat+megaraid,1\n/dev/nvme0")
	want := []ManualDevice{{"/dev/sda", "sat"}, {"/dev/bus/0", "sat+megaraid,1"}, {"/dev/nvme0", ""}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}

	r := mapRunner{
		"-i --json=c /dev/nvme0": `{"device":{"type":"nvme","protocol":"NVMe"},"model_name":"N"}`,
	}
	devs, err := Discover(context.Background(), r, DiscoverOptions{Manual: want[2:]}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(devs); !slices.Equal(got, []string{"/dev/nvme0|nvme|nvme|NVMe"}) {
		t.Errorf("got %v", got)
	}
}

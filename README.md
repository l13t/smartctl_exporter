# smartctl_exporter — Prometheus smartctl exporter (Go)

Drop-in Go replacement for the unmaintained
[matusnovak/prometheus-smartctl](https://github.com/matusnovak/prometheus-smartctl).
Metric names, labels, port and environment variables are the same, so existing
dashboards, alerts and recording rules keep working. The upstream Grafana
dashboard is included in [grafana/](grafana/grafana_dashboard.json).

Compatibility is checked by a test, not just claimed: `scripts/gen_golden.py`
runs the original Python code against a fake `smartctl` serving real JSON
fixtures (SATA HDD/SSD, USB bridge, NVMe, SAS, MegaRAID SATA+SAS, edge cases), and
`TestGoldenPythonCompat` requires the Go exporter to produce the same series with
the same values.

## Metrics

All per-drive metrics carry the Python label set:
`drive, type, model_family, model_name, serial_number, user_capacity`.

| Source | Metrics |
|---|---|
| ATA/SATA (`sat`, `usbjmicron`, `usbprolific`, `usbsunplus`, …) | `smartprom_<attribute>` (normalized value), `smartprom_<attribute>_raw` (raw value) |
| NVMe (`nvme`, `sntasmedia`, `sntjmicron`, `sntrealtek`) | `smartprom_<key>` for every key of `nvme_smart_health_information_log`, `smartprom_temperature_sensorN` |
| SCSI/SAS | `smartprom_<key>` / `smartprom_<key>_<subkey>` for integer fields |
| all | `smartprom_smart_passed` (1/0/-1), `smartprom_exit_code` |

Names follow the Python rule: lowercase, `-`/space/`/` become `_`, `.` is removed.
For example, `Reallocated_Sector_Ct` becomes `smartprom_reallocated_sector_ct_raw`.
Drives behind MegaRAID are labelled `drive="megaraid,N"`, with `type` set to `sat` or `scsi`.

New metrics (new names, so nothing existing changes):

- `smartprom_device_info{…, firmware_version, protocol, form_factor, rotation_rate, interface_speed, wwn, scsi_vendor, scsi_model_name, smartctl_version} 1`
- `smartprom_device_in_standby`: when `SMARTCTL_STANDBY_SKIP=true`.
- `smartprom_ext_*`: when `SMARTCTL_EXTENDED=true`, see below.
- `smartprom_exporter_{collect_duration_seconds,last_collect_timestamp_seconds,devices,smartctl_errors_total,build_info}`

### Extended mode: new smartctl features without code changes

With `SMARTCTL_EXTENDED=true`, each drive is also queried with `smartctl -x --json`,
and **every numeric or boolean field** in the output becomes a gauge named after
its JSON path. When smartctl adds a field, it shows up automatically:

```
smartprom_ext_nvme_smart_health_information_log_media_errors{drive="/dev/nvme0",…} 0
smartprom_ext_scsi_error_counter_log_read_total_uncorrected_errors{drive="/dev/sdd",…} 0
smartprom_ext_ata_device_statistics_pages_table_value{…,page="General Statistics",statistic="Power-on Hours"} 20071
smartprom_ext_ata_smart_self_test_log_standard_table_status_passed{…,index="0"} 1
```

Array elements are labelled: known tables get descriptive labels
(`attribute_id`/`attribute_name`, `page`/`statistic`, `event`). Other arrays use
the element's `name`, or else its `index`. Each array is capped at
`SMARTCTL_EXTENDED_MAX_ARRAY` elements. Strings are not exported; identity
strings go to `smartprom_device_info`. Noisy paths (`local_time`, build info,
attribute flag bits, WWN, PCI ids) are excluded by a regex that you can override.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `SMARTCTL_EXPORTER_ADDRESS` | `0.0.0.0` | same as Python |
| `SMARTCTL_EXPORTER_PORT` | `9902` | same as Python |
| `SMARTCTL_REFRESH_INTERVAL` | `60` | seconds (or Go duration like `5m`) |
| `SMARTCTL_METRICS_FILE_ENABLE` | `false` | write `<path>/smartctl.prom` for node_exporter's textfile collector |
| `SMARTCTL_METRICS_FILE_PATH` | `/metrics/` | |
| `SMARTCTL_RESCAN_INTERVAL` | `600` | rediscover drives; `0` = only at startup (Python behaviour) |
| `SMARTCTL_DEVICES` | — | skip `--scan-open`; whitespace-separated `PATH[:TYPE]`, e.g. `/dev/sda:sat /dev/bus/0:sat+megaraid,1 /dev/nvme0` |
| `SMARTCTL_DEVICE_INCLUDE` / `SMARTCTL_DEVICE_EXCLUDE` | — | regex on the `drive` label |
| `SMARTCTL_STANDBY_SKIP` | `false` | pass `-n standby` to ATA/SCSI drives. Sleeping drives are not woken, and their last values are re-exported |
| `SMARTCTL_EXTENDED` | `false` | enable `smartprom_ext_*` |
| `SMARTCTL_EXTENDED_EXCLUDE` | see `DefaultExtendedExclude` | regex on the flattened path |
| `SMARTCTL_EXTENDED_MAX_ARRAY` | `32` | |
| `SMARTCTL_PATH` | `smartctl` | |
| `SMARTCTL_COMMAND_TIMEOUT` | `30` | per smartctl call |
| `SMARTCTL_CONCURRENCY` | `4` | drives queried in parallel |
| `SMARTCTL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Endpoints: `/metrics`, `/healthz`. `smartctl_exporter -once` prints one collection to stdout.

smartctl runs in a background loop, as in Python. Scrapes only read the last
snapshot, so a slow drive never times out a scrape, and the scrape interval does
not affect how often disks are touched.

## Differences from the Python exporter

None of these differences rename or relabel an existing series.

1. Drives are queried with their real scan type (`-d usbjmicron`, `-d sat+megaraid,N`); Python always used `-d sat` / `-d nvme`.
2. Drives are rediscovered periodically. Series of removed drives disappear instead of staying stale forever. A failed smartctl call also drops that drive's series for the cycle and increments `smartprom_exporter_smartctl_errors_total`.
3. A drive that returns no attribute table still reports `smart_passed` and `exit_code`. Python threw a `KeyError` and dropped the drive.
4. Device types outside the Python lists (`ata`, `atacam`, `cciss,N`, `areca,N`, …) are handled by their reported protocol instead of being skipped.
5. Characters that are invalid in metric names become `_`. Python failed to register those metrics and lost the rest of that drive's attributes.
6. The same `megaraid,N` slot on two controllers no longer overwrites one another; the second becomes `drive="/dev/bus/1:megaraid,N"`.
7. `SMARTCTL_METRICS_FILE_ENABLE` is parsed as a boolean. In Python, any non-empty string, including `False`, enabled it.
8. The textfile contains only drive metrics, without `go_*`/`process_*`, so it does not collide with node_exporter.

Kept on purpose: for SAS drives that are not behind MegaRAID, `model_name` stays
`"Unknown"` with smartmontools ≥ r5286, exactly as in Python, so series identity
is preserved. The real name is available as `scsi_model_name` in
`smartprom_device_info`.

## Running

### Docker

```sh
docker compose up -d   # see docker-compose.yml
```

The image runs as UID 65532. `smartctl` carries file capabilities, so the
container needs `cap_add: [SYS_RAWIO, SYS_ADMIN]` plus `devices:` entries for the
drives. Without the capabilities, smartctl fails with `Operation not permitted`.
`privileged: true` also works if you prefer the old setup.
Platforms: 386, amd64, arm/v6, arm/v7, arm64, ppc64le, s390x.

### systemd

```sh
go build -o /usr/local/bin/smartctl_exporter ./cmd/smartctl_exporter
cp smartctl_exporter.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now smartctl_exporter
```

The unit runs under `DynamicUser` with only `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN` and `CAP_DAC_OVERRIDE`.

## Releases

Every push or merged PR to `master` runs CI. If any commit since the last tag is a
releasable [conventional commit](https://www.conventionalcommits.org/), a release
is cut automatically:

- `fix:` bumps the patch version, `feat:` the minor one, and `BREAKING CHANGE` the major one. Versions start at 0.1.0.
- `chore:`, `docs:`, `refactor:`, `test:` and `ci:` alone do not release.
- The release contains a git tag `vX.Y.Z` and a GitHub release with
  linux/freebsd/darwin binaries and `checksums.txt` (GoReleaser, `.goreleaser.yaml`).
- The image `ghcr.io/l13t/smartctl_exporter:X.Y.Z` (also `X.Y` and `latest`) is published for every platform listed above.

## Development

```sh
pre-commit install --install-hooks   # lint/format on commit, go test on push
pre-commit run --all-files

go test -race ./...
SMARTCTL_PATH=$PWD/testdata/fake-smartctl go run ./cmd/smartctl_exporter -once

# regenerate the golden file from the original Python code
pip install prometheus-client==0.19.0
python3 scripts/gen_golden.py
```

Fixtures in `testdata/scenario/` come mostly from
[prometheus-community/smartctl_exporter](https://github.com/prometheus-community/smartctl_exporter/tree/master/testdata)
(Apache-2.0). `synthetic-*.json` are hand-written edge cases. `devices.tsv` maps
fake device paths to fixtures and exit codes.

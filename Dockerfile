# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN case "$TARGETVARIANT" in v6) export GOARM=6 ;; v7) export GOARM=7 ;; esac; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/smartctl_exporter ./cmd/smartctl_exporter

FROM alpine:3.22
# smartctl gets the capabilities it needs as file capabilities, so the
# exporter itself runs unprivileged. The container must still be granted
# SYS_RAWIO (ATA/SCSI passthrough) and SYS_ADMIN (NVMe ioctls) via cap_add,
# plus access to the device nodes.
# Tracks the base image's current smartmontools; pin the alpine tag, not the package.
# hadolint ignore=DL3018
RUN apk add --no-cache smartmontools libcap-setcap \
    && setcap cap_sys_rawio,cap_sys_admin,cap_dac_override+ep /usr/sbin/smartctl \
    && apk del libcap-setcap
COPY --from=build /out/smartctl_exporter /usr/local/bin/smartctl_exporter
USER 65532:65532
EXPOSE 9902
ENTRYPOINT ["/usr/local/bin/smartctl_exporter"]

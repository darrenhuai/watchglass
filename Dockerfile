# Build stage: static binary, no cgo. Runs on the build host and
# cross-compiles for the target, so a multi-platform build doesn't emulate
# the Go toolchain under QEMU (slow, and flaky for arm/v7). TARGET* are
# filled in by BuildKit; under a plain single-platform build they're empty
# and Go falls back to the host, which is the same result.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
# What `watchglass -version` and the start-up line report. The publish
# workflow passes the release version; a local build says "dev".
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/watchglass ./cmd/watchglass

# Runtime: Debian slim for reliable ffmpeg/tesseract packaging (musl builds
# of both are a recurring source of subtle breakage).
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg tesseract-ocr ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1000 --home-dir /config --no-create-home watchglass \
    && install -d -o 1000 -g 1000 /config
COPY --from=build /out/watchglass /usr/local/bin/watchglass
USER watchglass
WORKDIR /config
VOLUME /config
EXPOSE 8080
# 0.0.0.0 inside the container is correct: reachability is decided by the
# port mapping. WATCHGLASS_IN_CONTAINER turns the "no auth on 0.0.0.0"
# start-up warning into a one-line note (it's expected here) and makes a
# refused 127.0.0.1 camera explain that 127.0.0.1 is the container. Add an
# auth: block before publishing the port beyond localhost.
ENV WATCHGLASS_IN_CONTAINER=1
# -healthcheck GETs / on the default -listen (127.0.0.1:8080, which the
# 0.0.0.0 listener also answers). The first readings take a few seconds,
# the web UI answers at once.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/watchglass", "-healthcheck"]
# A missing /config/config.yaml is created on first start (no watches);
# WATCHGLASS_DEMO=1 runs the built-in demo instead and leaves /config alone.
ENTRYPOINT ["/usr/local/bin/watchglass"]
CMD ["-config", "/config/config.yaml", "-db", "/config/watchglass.db", "-listen", "0.0.0.0:8080"]

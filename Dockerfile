# Build stage: static binary, no cgo.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/watchglass ./cmd/watchglass

# Runtime: Debian slim for reliable ffmpeg/tesseract packaging (musl builds
# of both are a recurring source of subtle breakage).
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg tesseract-ocr ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1000 --home /config watchglass
COPY --from=build /out/watchglass /usr/local/bin/watchglass
USER watchglass
WORKDIR /config
VOLUME /config
EXPOSE 8080
# 0.0.0.0 inside the container is correct: reachability is decided by the
# port mapping. Add an auth: block before publishing the port beyond localhost.
ENTRYPOINT ["/usr/local/bin/watchglass"]
CMD ["-config", "/config/config.yaml", "-db", "/config/watchglass.db", "-listen", "0.0.0.0:8080"]

# Installing watchglass

watchglass is one program with a web UI. Run it in Docker, as the Home Assistant add-on, or as a plain binary. Every way reads the same `config.yaml`, so you can move between them.

- [Docker Compose](#docker-compose)
- [docker run](#docker-run)
- [Home Assistant add-on](#home-assistant-add-on)
- [Binaries](#binaries)
- [What to install for reading text and streams](#what-to-install-for-reading-text-and-streams)
- [Run it as a service (systemd)](#run-it-as-a-service-systemd)
- [go install and building from source](#go-install-and-building-from-source)
- [Command-line flags](#command-line-flags)

## Docker Compose

```bash
mkdir -p watchglass/config && cd watchglass
curl -fsSLO https://raw.githubusercontent.com/darrenhuai/watchglass/master/docker-compose.yml
PUID=$(id -u) PGID=$(id -g) docker compose up -d
```

Open http://127.0.0.1:8080 and add a watch.

- **The image** is `ghcr.io/darrenhuai/watchglass`, built for linux/amd64, linux/arm64 and linux/arm/v7 (about 230 MB, 207 MB on arm/v7). It includes ffmpeg and tesseract. It doesn't include Python, so `engine: rapidocr` isn't available in it. `:latest` is the newest release, and every release also has its own tag (`:0.1.8`).
- **Your files** live in `./config`: `config.yaml` and the history database `watchglass.db`. A missing `config.yaml` is created on first start, with no watches.
- **PUID and PGID** make the container run as you, so it can write to `./config`. Without them it runs as uid 1000, which is fine if that's you. On a NAS (Synology, Unraid, TrueNAS) your uid is often something else, and you'd get a "permission denied" naming the uid it runs as.
- **The port** is bound to `127.0.0.1` only. Before you change the `ports:` line to reach it from other machines, add an [`auth:` block](security.md).
- **A camera on the Docker host itself**: inside the container, `127.0.0.1` is the container. Use `http://host.docker.internal:<port>/...` instead. The compose file maps that name on Linux too.
- **Try the demo** in the same setup: uncomment the `WATCHGLASS_DEMO=1` lines in the compose file. The demo keeps its files in the container's `/tmp` and leaves `./config` alone.
- **Health**: the image's `HEALTHCHECK` runs `watchglass -healthcheck`, which checks `127.0.0.1:8080` inside the container. If you change the listen port in the container's command, pass the same `-listen` to the check with `--health-cmd` (or `healthcheck:` in compose), or Docker will call it unhealthy.
- **Updating**: `docker compose pull && docker compose up -d`.

To build the image yourself from a clone instead, comment out `image:` and uncomment `build: .` in the compose file, or run `docker build -t watchglass .`.

## docker run

```bash
mkdir -p watchglass/config
docker run -d --name watchglass --restart unless-stopped \
  -p 127.0.0.1:8080:8080 -v "$PWD/watchglass/config:/config" \
  --add-host host.docker.internal:host-gateway \
  --user "$(id -u):$(id -g)" ghcr.io/darrenhuai/watchglass:latest
```

The demo, with nothing to clean up afterwards:

```bash
docker run --rm -p 127.0.0.1:8080:8080 -e WATCHGLASS_DEMO=1 ghcr.io/darrenhuai/watchglass
```

In a container the start-up line prints the address inside the container (`http://127.0.0.1:8080/`). Which port you open on your machine is decided by `-p`, so keep it `8080:8080` unless you have a reason not to.

## Home Assistant add-on

[![Add the watchglass add-on repository to your Home Assistant](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fdarrenhuai%2Fwatchglass)

Or add `https://github.com/darrenhuai/watchglass` under **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, then install **watchglass**. The add-on is experimental: it runs the same image on amd64 and aarch64, with no ingress, so the dashboard is at `http://<home-assistant-host>:8080`. Read [addon/DOCS.md](../addon/DOCS.md) before you start it, because that port is open to your whole network.

## Binaries

Each release has an archive per system. The names don't change between releases, so these links always fetch the newest one:

| System | Archive |
|---|---|
| Linux x86-64 | [watchglass_linux_amd64.tar.gz](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_linux_amd64.tar.gz) |
| Linux ARM64 (Raspberry Pi 4/5 on a 64-bit OS) | [watchglass_linux_arm64.tar.gz](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_linux_arm64.tar.gz) |
| Linux ARMv7 (32-bit Raspberry Pi OS) | [watchglass_linux_armv7.tar.gz](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_linux_armv7.tar.gz) |
| macOS Apple silicon | [watchglass_darwin_arm64.tar.gz](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_darwin_arm64.tar.gz) |
| macOS Intel | [watchglass_darwin_amd64.tar.gz](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_darwin_amd64.tar.gz) |
| Windows x86-64 | [watchglass_windows_amd64.zip](https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_windows_amd64.zip) |

Each unpacks into a folder of the same name, holding the binary, `LICENSE` and `README.md` (and `watchglass.service` on Linux). `checksums.txt` on the release page has their SHA-256 sums.

On Linux, in one line:

```bash
curl -fsSL https://github.com/darrenhuai/watchglass/releases/latest/download/watchglass_linux_amd64.tar.gz | tar xz && ./watchglass_linux_amd64/watchglass
```

On first run watchglass creates an empty `config.yaml` in the current folder, then logs a line like `watchglass 0.1.8 ready: open http://127.0.0.1:8080/`. Add `-demo` to try it on the built-in cameras first; the demo keeps its own config in the temp folder and never touches `config.yaml`.

**Windows.** Double-click `watchglass.exe`. It creates `config.yaml` next to itself and opens the web UI in your browser; closing its window stops it. Double-click it again while it runs and it just opens the browser. If Windows says "Windows protected your PC", that's because the exe isn't code-signed: click **More info → Run anyway**. From a terminal, stop it with Ctrl+C. Task Manager and `taskkill /F` end it without a clean shutdown (the history database and the MQTT "offline" message aren't closed properly).

**macOS.** A binary downloaded with a browser is quarantined and macOS refuses to open it. `curl` downloads aren't. To clear the flag: `xattr -d com.apple.quarantine ./watchglass`.

**Which version is this?** `watchglass -version`.

## What to install for reading text and streams

| You want to | Install |
|---|---|
| Read text (`engine: tesseract`, the default) | tesseract: `sudo apt install tesseract-ocr`, `brew install tesseract`, or `winget install UB-Mannheim.TesseractOCR` |
| Read seven-segment digits (`engine: sevenseg`) | nothing, it's built in |
| Read hard text better (`engine: rapidocr`) | Python with `pip install rapidocr onnxruntime` |
| Use `rtsp://`, `v4l2:`, `dshow:` or `ffmpeg:` sources | ffmpeg: `sudo apt install ffmpeg`, `brew install ffmpeg`, or `winget install Gyan.FFmpeg` |
| Use `http(s)://` snapshot and MJPEG sources | nothing |

watchglass looks for tesseract on PATH first. The Windows installer puts it in `C:\Program Files\Tesseract-OCR` without adding it to PATH, so watchglass also looks in `%ProgramFiles%`, `%ProgramFiles(x86)%` and `%LOCALAPPDATA%\Programs` (each `\Tesseract-OCR\tesseract.exe`), and on macOS in `/opt/homebrew/bin` and `/usr/local/bin`. Anywhere else, pass `-tesseract /path/to/tesseract`. Restart watchglass after installing an engine; it looks once, at start-up, and logs what it found in one line:

```text
engines: tesseract=C:\Program Files\Tesseract-OCR\tesseract.exe, sevenseg=built-in, rapidocr=not installed
```

For rapidocr, watchglass tries `python3` and then `python`, and keeps the first that can import `rapidocr` and `onnxruntime`. `-python /path/to/venv/bin/python` picks one. Each read starts Python and loads the models (2-4 s on a desktop, more on a Pi), so give rapidocr watches an interval of 10 s or more.

## Run it as a service (systemd)

The Linux archives include `watchglass.service`. As root, from the unpacked folder:

```bash
install -m 0755 watchglass /usr/local/bin/watchglass
useradd --system --home-dir /var/lib/watchglass --shell /usr/sbin/nologin watchglass
usermod -aG video watchglass   # only for v4l2: (USB camera or capture card) sources
install -m 0644 watchglass.service /etc/systemd/system/watchglass.service
systemctl daemon-reload && systemctl enable --now watchglass
```

The config is created at `/var/lib/watchglass/config.yaml`. Follow the log with `journalctl -u watchglass -f`. The unit listens on `127.0.0.1:8080`; to reach it from other machines, add an `auth:` block first, then change `-listen` in the unit (see [security.md](security.md)). The unit is also in the repo as [examples/watchglass.service](../examples/watchglass.service).

## go install and building from source

With Go 1.27 or newer (an older Go downloads 1.27 for you):

```bash
go install github.com/darrenhuai/watchglass/cmd/watchglass@latest
```

That puts `watchglass` in `$(go env GOPATH)/bin`. From a clone:

```bash
git clone https://github.com/darrenhuai/watchglass && cd watchglass
go build ./cmd/watchglass
go test ./...
```

The build needs no C compiler (`CGO_ENABLED=0` works); the SQLite driver is pure Go. To run it from the clone without building: `go run ./cmd/watchglass -demo`.

## Command-line flags

| Flag | Default | What it does |
|---|---|---|
| `-config` | `config.yaml` | The config file. Created, with no watches, if it doesn't exist. |
| `-db` | `watchglass.db` | The SQLite history database. Created if missing. |
| `-listen` | `127.0.0.1:8080` | Where the web UI listens. See [security.md](security.md) before widening it. |
| `-base-path` | none | A URL prefix, for a reverse proxy that serves watchglass under a path. See [security.md](security.md#behind-a-reverse-proxy). |
| `-tesseract` | found automatically | The tesseract binary, when it isn't on PATH or in the usual install folders. |
| `-python` | `python3`, then `python` | The Python that has rapidocr. |
| `-demo` | off | Run the built-in demo: two fake cameras and two watches, with their own config in the temp folder, reset on every start. `-config` and `-db` are ignored. The environment variable `WATCHGLASS_DEMO=1` does the same. |
| `-healthcheck` | | Exit 0 if a watchglass answers on `-listen`, 1 if not. For container health checks. |
| `-version` | | Print the version and exit. |

If the port is taken, watchglass says so and suggests the next one, for example `watchglass -listen 127.0.0.1:8081`.

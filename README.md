# watchglass

**changedetection.io for video streams.** Point it at any screen — a 3D
printer's LCD, a lab instrument, a server console — draw a region, and get a
push notification when that region's text or pixels change. Self-hosted, one
binary, nothing leaves your network.

> Early development. Core engine works headless via YAML config; web UI,
> RTSP/ffmpeg sources, and MQTT/Home Assistant discovery are next.

## Quick start

1. Install [tesseract](https://github.com/tesseract-ocr/tesseract) (only
   needed for OCR triggers): `apt install tesseract-ocr` or
   `choco install tesseract`.
2. Copy `examples/config.yaml`, point `source` at your camera's snapshot URL,
   adjust the region and trigger.
3. Run:

    go run ./cmd/watchglass -config config.yaml

## Trigger types

| type | fires when |
|---|---|
| `ocr_match` | region text matches a regex (edge-triggered) |
| `ocr_changed` | region text changes to a new stable value |
| `numeric` | a number extracted from region text crosses a threshold |
| `pixel_change` | more than `threshold`% of region pixels change |

All OCR triggers require `confirm` consecutive identical readings before a
state is believed (filmed screens flicker), and every trigger respects
`cooldown`.

## License

MIT

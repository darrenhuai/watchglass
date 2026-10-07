# Contributing to watchglass

Thanks for looking. The most useful thing you can bring is a screen: a device you want watched, and a frame of it.

## Ask for a recipe

[Open a recipe request](https://github.com/darrenhuai/watchglass/issues/new?template=recipe.yml) with the device, what's on its screen, how you're capturing it and what should trigger an alert. Attach a frame: a snapshot from your camera, or a phone photo of the panel. Crop out anything personal. A real frame makes the difference between a recipe that works on your device and one that works on a drawing of it.

The camera URLs in the [cookbook](docs/recipes/camera-urls.md) marked "from vendor docs" haven't been run against the real device. If you have one, a comment saying it works (or what does) helps everyone after you.

## Report a bug

[Open a bug report](https://github.com/darrenhuai/watchglass/issues/new?template=bug.yml). The form asks for the output of `watchglass -version`, how you installed it, and the log around the problem. Take passwords and tokens out of the log and config first. watchglass hides camera passwords on its pages, but `config.yaml` and your own shell history don't.

Security problems: see [docs/security.md](docs/security.md#reporting-a-security-problem).

## Develop

You need Go 1.27 or newer. ffmpeg and tesseract are optional; the tests that need them skip when they're missing.

```bash
git clone https://github.com/darrenhuai/watchglass && cd watchglass
go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...
go run ./cmd/watchglass -demo
```

CI runs the same checks on Linux and the tests again on Windows, both with `CGO_ENABLED=0`. Keep them passing.

How the code is laid out:

- `cmd/watchglass`: flags, start-up, the demo.
- `internal/source`: getting a frame (HTTP, MJPEG, ffmpeg, the demo cameras).
- `internal/imgproc`, `internal/ocr`: cropping, preprocessing, tesseract, the seven-segment decoder, rapidocr.
- `internal/trigger`, `internal/runner`, `internal/supervisor`: deciding when a watch fires, and running the watches.
- `internal/notify`, `internal/hass`: notifications and MQTT.
- `internal/web`: the web UI. Go `html/template` templates, one hand-written `style.css`, and plain JavaScript in `app.js` and `index.js`. No framework, no build step, nothing loaded from a CDN.

A few rules the project keeps:

- The standard library first. A new dependency needs a good reason.
- The tests assert the rendered HTML and the exact wording. When you change what a page says, change the test on purpose to the new text.
- Tests that read a file from the checkout must cope with CRLF line endings, because Windows CI checks files out that way.
- Never put a real-looking webhook URL or token in a test or a doc, not even a vendor's example: GitHub's push protection blocks the push. Build them at run time in tests (`"hooks.slack" + ".com/..."`), and write `<TOKEN>` in docs.
- Text the UI shows should read like a person wrote it: plain and specific.

## Add or change a recipe

Recipes live in [docs/recipes](docs/recipes/). Each one has:

1. A complete config in a ```` ```yaml ```` block starting with `watches:`. `go test ./internal/docscheck` loads every such block the way `watchglass -config` does.
2. A sample frame in [docs/recipes/samples](docs/recipes/samples/). Only frames you have the right to share, of devices, with nothing personal in view. No photos of people.
3. A check that ties them together, as an HTML comment next to the config:

   ```html
   <!-- sample-check: heat-pump-fault samples/heat-pump-fault.jpg met -->
   ```

   The test crops the frame to the watch's region, reads it with the watch's engine, and checks that the trigger's condition is `met` or `not-met`, as Test this region would. Add `reads=<text>` to check the exact text (only for `sevenseg`, whose output doesn't depend on the tesseract version). Checks for tesseract skip when it isn't installed, and rapidocr checks skip without Python.
4. A screenshot of Test this region on the sample, in `docs/recipes/img/`.

Point the watch at the frame to take the screenshot: run watchglass from the repository folder with a watch whose source is `ffmpeg:-i docs/recipes/samples/<frame>`.

The same test checks every relative link and `#anchor` in the docs, that no doc uses an indented code block (they get no copy button on GitHub), and that links from the web UI into the docs point at files that exist.

## Record the demo GIF and screenshots

`docs/demo.gif` and the stills in `docs/img/` are recorded from the real UI. Re-record them when the UI changes what they show.

- **Use the demo cameras.** Run `watchglass -demo` for the GIF. It needs no camera and fires about 20 seconds after it starts, because each demo camera plays a 40-second loop that holds its last frame (PRINT COMPLETE, 25.3) from 20 s to 40 s.
- **Hide your machine.** The demo banner shows the demo's folder under the temp directory, so point `TMP` and `TEMP` (Windows) or `TMPDIR` at a neutral folder like `C:\Temp` first. For the stills, run a normal config whose watches use `source: demo:printer` and `demo:sevenseg`; without `-demo` there's no banner.
- **Show a real notification.** Send the test notification and the fire to a local receiver, not a public ntfy topic. A few lines of Python's `http.server` that answer 200 to POST and PUT will do. To show a realistic URL like `ntfy+http://ntfy.lan/printer-done`, start watchglass with `HTTP_PROXY` pointing at the receiver: Go sends any non-loopback host through the proxy without resolving it.
- **Script the browser.** A headless Edge or Chrome (Playwright works) in the dark colour scheme at 960x640, one screenshot per step: set the pattern, drag a new box (start the drag outside the existing box, or you'll resize it), Test this region, paste the notify URL, Send test notification, Save & restart watch, then wait for FIRED · SENT. Save has to land before the 20-second mark, or the fire happens on the held frame with no change on screen. Headless screenshots have no mouse pointer, so draw one in.
- **Make the GIF with ffmpeg**, from the numbered frames, at 10 fps with a single palette:

  ```bash
  ffmpeg -framerate 10 -i s%04d.png -vf palettegen=max_colors=256:stats_mode=full palette.png
  ffmpeg -framerate 10 -i s%04d.png -i palette.png -lavfi paletteuse=dither=none:diff_mode=rectangle -loop 0 demo.gif
  ```

  Keep it under about 15 seconds and 500 KB. Held frames are duplicates, which the rectangle diff makes nearly free.
- **Stills**: 1440 px wide in the dark theme, cropped to the content column, quantized to 256 colours. Update the alt text in the README to say what the new picture shows.

## Roadmap

Roughly in order. If you want to work on one, open an issue first so we can agree on the shape.

- The Home Assistant add-on in the official store (it installs as a custom repository today).
- Template matching triggers ("this icon appeared").
- Several regions in one condition ("A says DONE and B is above 200").
- An optional vision-model engine, off by default and clearly labelled.

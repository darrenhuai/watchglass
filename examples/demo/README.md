# watchglass demo rig

## The quick way: `watchglass -demo`

```
watchglass -demo
```

That's all it takes: just the binary, with no camera, no Python and no clone. It starts
two fake cameras built into watchglass and two watches on them, then prints
the address to open:

- `demo-printer` reads a 3D printer's LCD (`source: demo:printer`) and
  fires when it says `PRINT COMPLETE`. That needs tesseract. Without it,
  the watch fires when the pixels flip to the inverted "complete" screen.
- `demo-scale` reads a seven-segment readout with the built-in decoder
  (`source: demo:sevenseg`) and fires when it goes above 25.

Each camera plays a 40 second loop: five frames 4 seconds apart, then the
last frame held for 20 seconds. Both watches fire about 20 seconds after
the start, and again on every loop after their 30 s cooldown.

The demo writes its own config and history to a `watchglass-demo` folder
in the temp directory (`watchglass-demo-<uid>` on Linux and macOS, where
that directory is shared) and starts them fresh every time. It never creates or
changes a `config.yaml` in the current directory. Docker works the same way:

```
docker run --rm -p 127.0.0.1:8080:8080 -e WATCHGLASS_DEMO=1 ghcr.io/darrenhuai/watchglass
```

With no watches yet, the web UI's empty state also has an **Add a demo
watch** button. It creates `demo-printer` on the built-in camera so you can
draw the region yourself.

The rest of this page is the older rig: a fake camera served over HTTP by
a Python script, for recording the launch GIF and for testing the HTTP
snapshot path end to end.

## The frames

`internal/demo/frames/printer/frame_0.png` .. `frame_5.png` are six
pre-rendered 640x360 "LCD" screens: `PRINTING 12%` through `PRINTING 94%`,
then `PRINT COMPLETE` in inverted colors. They were generated once with a
throwaway Pillow script (PIL, `ImageFont.truetype` on `consola.ttf`) and
committed as-is. The rig has no runtime image-generation dependency. They
live next to the Go code that embeds them, and `fakecam.py` serves the same
files.

## Run it

Terminal 1, the fake camera:

```
python examples/demo/fakecam.py
```

It serves `http://127.0.0.1:8100/snapshot.jpg` and cycles frames every 5s
(`--period` changes that). The `.jpg` path serves PNG bytes on purpose:
watchglass decodes by sniffing content, not by extension, and so do most
real IP cameras that lie about their format.

Terminal 2, watchglass:

```
go run ./cmd/watchglass -config examples/demo/config.yaml -db demo.db -listen 127.0.0.1:8123
```

Open http://127.0.0.1:8123/watch/demo-printer and watch the live readout
change as the frames cycle. `config.yaml` uses an `ocr_match` trigger, so it
needs `tesseract` on PATH (`apt install tesseract-ocr` / `choco install
tesseract`), or `engine: rapidocr` with a Python that has `pip install
rapidocr onnxruntime`. Without one of them watchglass refuses to start a
text watch. The file has a commented-out `pixel_change` block that needs
neither.

## Recording the launch GIF

These are the beats to record with this rig instead of a real printer:

1. Open `demo-printer`'s live preview and drag a rectangle around the status
   line. The region is already set, but the drag is the visual beat.
2. Show the trigger picker set to a match rule, with the live readout
   underneath showing it really reads `PRINTING NN%`.
3. Kill terminal 1 and restart it with `--once-complete`, so the printer
   finishes exactly once. Then let the timelapse jump to `PRINT COMPLETE`
   and the notification land.

## Seven-segment display rig

`internal/demo/frames/sevenseg/frame_0.png` .. `frame_5.png` are six
640x240 frames of a four-position red LED readout climbing `23.5`, `23.7`,
`24.1`, `24.6`, `25.0`, `25.3`, with the leading position unlit. They have
hexagonal bars, real gaps between bars, the unlit ghost segments every LED
display shows, a decimal point, LED glow and soft edges, the way a bench
scale or thermometer looks to a camera. Like the printer frames, they were
generated once with a throwaway Pillow script (palettised to keep them
small) and committed as-is. The script is not part of the repo.

Serve them instead of the printer frames:

```
python examples/demo/fakecam.py --frames sevenseg
```

(`--frames` also takes a directory. The old `examples/demo/frames-sevenseg`
path still works too.) Then run watchglass against the matching config:

```
go run ./cmd/watchglass -config examples/demo/config-sevenseg.yaml -db demo.db -listen 127.0.0.1:8123
```

`config-sevenseg.yaml` is a `numeric` watch with `engine: sevenseg`, the
built-in seven-segment decoder. It needs no tesseract and no preprocessing.
Open http://127.0.0.1:8123/watch/demo-scale, press **Test this region** to
see the four glyphs and their confidences, and watch the readout climb. The
trigger (`op: gt`, `threshold: 25`) fires when the frames reach `25.3`.

## Cleanup

```
rm demo.db
```

Then press Ctrl-C in both terminals. Nothing else here is generated at
runtime.

## Feeding a container

Running watchglass in Docker instead? Start the camera on every interface
and point the watch at the host:

    python fakecam.py --bind 0.0.0.0

and use `http://host.docker.internal:8100/snapshot.jpg` as the source (the
compose file maps `host.docker.internal` to the host on Linux too).

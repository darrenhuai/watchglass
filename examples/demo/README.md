# watchglass demo rig

A fake printer camera plus a matching config, so you can see (and record) a
watch fire without owning a real printer or camera.

`frames/frame_0.png` .. `frame_5.png` are six pre-rendered 640x360 "LCD"
screens: `PRINTING 12%` through `PRINTING 94%`, then `PRINT COMPLETE` in
inverted colors. They were generated once with a throwaway Pillow script
(PIL, `ImageFont.truetype` on `consola.ttf`) and committed as-is — the rig
has no runtime image-generation dependency.

## Run it

Terminal 1 — the fake camera:

```
python examples/demo/fakecam.py
```

Serves `http://127.0.0.1:8100/snapshot.jpg`, cycling frames every 5s
(`--period` to change it). It's a `.jpg` path serving PNG bytes on purpose —
watchglass decodes by sniffing content, not by extension, same as most real
IP cameras that lie about their format.

Terminal 2 — watchglass:

```
go run ./cmd/watchglass -config examples/demo/config.yaml -db demo.db -listen 127.0.0.1:8123
```

Open http://127.0.0.1:8123/watch/demo-printer and watch the live readout
change as frames cycle.

## Recording the launch GIF

Mirrors PRD §10's beats, using this rig instead of a real printer:

1. Open `demo-printer`'s live preview; drag a rectangle around the status
   line (region is already set, but the drag is the visual beat).
2. Show the trigger picker snapping to a match rule, with the live readout
   underneath proving it's really reading `PRINTING NN%`.
3. Kill terminal 1, restart it with `--once-complete` so the printer finishes
   exactly once, then let the timelapse jump to `PRINT COMPLETE` and the
   fired notification land.

For the OCR money-shot (matching the actual text, not just pixel motion),
swap `config.yaml`'s trigger for the commented-out `ocr_match` block — it
needs `tesseract` on PATH (`apt install tesseract-ocr` / `choco install
tesseract`); without it watchglass refuses to start a non-pixel watch.

## Cleanup

```
rm demo.db
```

(and Ctrl-C both terminals). Nothing else here is generated at runtime.

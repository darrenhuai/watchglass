# Closed-firmware printer LCD

**Read this first: if your printer has an API, use it instead.** OctoPrint,
Moonraker (Klipper), and most vendor local APIs give you structured print
state — percentage, ETA, error codes — with no camera to mount, no glare to
fight, and no OCR to tune. This recipe exists for the printers that don't:
closed-firmware machines like Bambu Lab units running in fully LAN-only mode
(cloud disabled), whose local protocol is undocumented and not something
this project reverse-engineers. If the touchscreen is the only place the
printer tells you anything, pointing a camera at it is what's left.

## Watches config

```yaml
watches:
  - name: printer-lcd
    # A camera pointed at the printer's touchscreen — see
    # generic-snapshot-camera.md for finding this URL if it's an IP camera,
    # or rtsp-camera.md if it's RTSP-only.
    source: http://192.168.1.55/snapshot.jpg
    interval: 10s
    region: {x: 0.20, y: 0.35, w: 0.60, h: 0.15}
    preprocess:
      grayscale: true
      threshold: 130
    trigger:
      type: ocr_match
      pattern: "(?i)print complete|error"
      confirm: 3        # require 3 consecutive identical readings first
      cooldown: 2h       # a print run is long; don't re-notify on flicker
    notify:
      - ntfy://ntfy.sh/example-bambu
```

## Tuning notes

`ocr_match` is edge-triggered: it only fires the moment the pattern goes
from *not matching* to *matching*, not on every poll while the screen still
says "Print complete." That's why a single pattern can safely cover two
states (`print complete` or `error`) without spamming — each is its own
edge. The notification body carries the OCR reading, but not which branch
matched; if you need to tell success from failure at a glance, split them
into two watches with two patterns and two `notify` topics instead.

`confirm: 3` matters more here than almost anywhere else in this gallery:
touchscreen photos are exactly the kind of filmed-screen shot most prone to
glare, camera-shake blur, and moiré from the screen's own refresh — a single
bad frame reading "Print complete" (or misreading something else as it) is
a false positive you don't want. Three consecutive identical reads before
believing a transition filters almost all of that out.

`cooldown: 2h` is deliberately long. A completed print sits on the screen
for a while before anyone starts the next one, and this stops a printer
that flickers between "Print complete" and a status screen (common on some
firmware) from re-firing every time it repaints.

## Caveats

- This is screen-scraping a physical touchscreen through a camera —
  lighting, angle, and screen glare are the dominant source of missed or
  false reads, more so than for a printed-paper or e-ink display. Test at
  the actual lighting conditions you care about (workshop lights off at
  night is a common miss), not just once at setup time.
- A firmware update that changes the touchscreen's layout, font, or wording
  will silently break the region and pattern — nothing here detects that
  for you. Glance at the web UI's live reading strip occasionally after an
  update.
- If your printer *does* expose Moonraker or OctoPrint (most Klipper-based
  builds, and any printer you've flashed OctoPrint onto), skip this recipe
  entirely — poll their status APIs directly, or use their own
  notification plugins, which know the actual print state rather than
  guessing it from pixels.

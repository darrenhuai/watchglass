# Lab instrument seven-segment readout

Bench scales, multimeters, thermometers, older gauges — a lot of lab and
shop instruments still show their reading on a seven-segment LED or LCD
display: digits built from a handful of straight bars rather than a normal
font's continuous strokes. **Be honest with yourself about accuracy going
in**: tesseract's OCR models are trained on regular print, and true
seven-segment glyphs (a "0" is six disconnected bars with gaps, not a solid
loop) confuse it more than printed text does. This recipe is the current
best-effort approach, not a solved problem — a dedicated seven-segment
decoder (geometry-based, not a general OCR font model) is on the project
roadmap and should do meaningfully better once it exists.

## Preprocessing walkthrough

Do this in the web UI's calibration loop: open the watch, drag a region
around *just the digits* (exclude units, labels, decorative text), and hit
**Test this region** after every slider change so you're tuning against a
live OCR result, not guessing. The sliders build on each other, so go in
this order:

1. **`grayscale: true`** — flattens color first. Colored LED/LCD faces
   (red, green, blue digit displays are all common) otherwise leave you
   fighting channel noise before you've even gotten to contrast.
2. **`invert: true`** if the display is bright digits on a dark
   background — most LED and backlit LCD readouts are. Tesseract expects
   dark text on a light background; inverting flips the crop so segments
   read like ink on paper instead of the reverse.
3. **Binarize** (the `threshold` field in YAML) — binarize once the
   polarity is right. Start around 120–160 and slide until the glow around
   each lit segment collapses to a crisp edge with no soft halo. This
   matters more here than for any other recipe: an LED segment's glow
   radius is often wider than the gap to its neighbor, so too low a value
   fuses adjacent segments (or whole digits) into a blob no OCR model can
   read.
4. **`upscale: 2`–`4`** — instrument readouts are usually a small patch of
   the frame. Nearest-neighbor upscaling hands tesseract more pixels to
   tell a segment from the 1-pixel gap next to it.

## Watches config

```yaml
watches:
  # Primary: numeric trigger with a generous pattern and low confirm.
  - name: lab-scale-reading
    source: http://192.168.1.80/snapshot.jpg
    interval: 5s
    region: {x: 0.40, y: 0.35, w: 0.22, h: 0.10}
    preprocess:
      grayscale: true
      invert: true
      threshold: 140
      upscale: 3
    trigger:
      type: numeric
      pattern: "([0-9]{1,4}(?:\\.[0-9]+)?)"   # generous: 1-4 digits, optional full decimal
      op: gt
      threshold: 500
      confirm: 1
      cooldown: 1m
    notify:
      - ntfy://ntfy.sh/example-lab-scale

  # Fallback: skip OCR entirely, just detect that the reading changed.
  - name: lab-scale-any-change
    source: http://192.168.1.80/snapshot.jpg
    interval: 5s
    region: {x: 0.40, y: 0.35, w: 0.22, h: 0.10}
    trigger:
      type: pixel_change
      threshold: 5
      cooldown: 30s
    notify:
      - ntfy://ntfy.sh/example-lab-scale
```

## Tuning notes

`confirm: 1` on the numeric watch is a deliberate trade, not an oversight.
The trigger only advances toward "stable" when a poll's OCR text exactly
matches the previous poll's — a seven-segment misread (one glitchy digit
one time in ten) makes that exact-match bar hard to clear at any `confirm`
above 1 or 2, and a legitimately changing reading (the whole point of a
scale or gauge) resets the count just as often. Trade a *slightly* higher
chance of firing on a single OCR glitch for actually firing at all, and
lean on `cooldown` to absorb a stray false trigger instead. Raise `confirm`
only if you've watched the reading strip and seen the same glitch repeat.

The `pattern` here is intentionally loose — `[0-9]{1,4}` with an optional
decimal — because a stricter pattern that assumes a fixed digit count will
simply fail to match on any poll where one digit misreads as a letter or
drops a segment, and a `numeric` trigger with no match just doesn't fire
that poll (it doesn't error). Looser patterns fail less often; they also
occasionally let garbage through, which is what `confirm` and `cooldown`
exist to filter.

Get the decimal part of the pattern right, not just the integer part: an
earlier version of this pattern used `\.?[0-9]?` — an optional dot followed
by *at most one* digit — which silently truncates any reading with two or
more decimal digits ("500.36" extracts as 500.3, not 500.36). That's a
value close enough to the real one to look plausible in a notification but
wrong enough to misfire near a threshold, and nothing about it looks like
an error — the trigger fires normally, just on the wrong number. The
pattern above, `(?:\.[0-9]+)?`, captures every digit after the dot instead
of just the first.

`pixel_change` is the honest fallback when you don't actually need the
number, just to know something changed — it compares raw pixels, so
`preprocess` doesn't apply to it at all (preprocessing is OCR-only; pixel
triggers always diff the untouched crop). No font model, no misreads, just
"this many pixels flipped." Use it as watch #2 on the same region so you
have a signal that keeps working even when the numeric watch is between
misreads.

## Caveats

- Expect a real error rate. Similar-looking digit pairs (8/0, 1/7, 6/8 at
  low resolution) are the common failure mode — don't wire this straight to
  anything where a wrong reading is dangerous or costly.
- Viewing angle matters more for seven-segment displays than for text LCDs:
  a segment can go fully invisible off-axis, not just blurry. Mount the
  camera as close to head-on as you can.
- There's no dedicated seven-segment decoder in watchglass today (it's on
  the roadmap) — everything above is tuning a general-purpose OCR engine to
  do a job it wasn't built for. Treat any given set of preprocessing values
  here as a starting point for your specific instrument, camera, and
  lighting, not a value to copy verbatim and trust.
- The value a `numeric` trigger fires on is whatever your `pattern`'s
  capture group extracted, not necessarily what the display actually shows
  — a pattern that's too narrow silently truncates or drops digits (as
  above), and one that's too loose can grab a stray digit from a unit
  label or neighboring reading in the same crop. Watch the reading strip
  against the physical display for a while after any pattern change, not
  just once, before trusting the extracted value near a threshold.

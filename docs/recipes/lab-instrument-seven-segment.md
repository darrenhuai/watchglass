# Lab instrument seven-segment readout

Bench scales, multimeters, thermometers, older gauges — a lot of lab and
shop instruments still show their reading on a seven-segment LED or LCD
display: digits built from a handful of straight bars rather than a normal
font's continuous strokes. Tesseract reads those badly (a "0" is six
disconnected bars with gaps, not a solid loop), which is why watchglass has
a decoder built for them: set `engine: sevenseg` on the watch and the
digits are read by geometry — find the bars, check the seven segment
positions of each cell — with no font model, no external binary, and no
preprocessing to tune. This recipe is that setup.

## What the decoder does

- Works out the polarity itself: lit red/green/blue digits on a dark face
  and dark digits on a light LCD both read as-is. Leave `preprocess` off
  unless a test shows you need it.
- Copes with the gaps real displays leave between bars, with glow around
  lit LEDs, with the unlit "ghost" segments most LED displays show, and
  with any digit size from a ~40px-tall crop up.
- Reads `0`-`9`, a leading `-`, and the decimal point, joined into one
  string: `23.5`, `-8.0`, `1234`. A glyph whose bars spell no digit comes
  back as `?`.
- **Test this region** shows one chip per glyph with its confidence (how
  far every segment sat from the on/off line): a clean read scores near
  100, a glyph with a half-lit or half-hidden bar scores low. Anything
  below 60 shows red — that's the digit to worry about.

## Set it up in the web UI

1. Add the watch, open it, and drag a rectangle around *just the digits*.
   Leave out units, labels, the decimal-point-only annunciators some meters
   have, and any bezel — a bright frame edge inside the crop looks like a
   bar. Include the whole height of the digits with a little margin; a
   crop that clips the top or bottom bar changes what the digits look like.
2. In the Trigger fieldset set **Engine** to `sevenseg` and the Type to
   `numeric` (or `ocr_changed` if you just want to know the reading moved).
3. Hit **Test this region**. You should see the reading and one chip per
   glyph. If a digit reads `?` or scores low, the usual causes are in
   [Caveats](#caveats) below.
4. Set Pattern, Compare (above/below) and Threshold, and **Save**.

## Watches config

```yaml
watches:
  # Primary: read the number, fire above a limit.
  - name: lab-scale-reading
    source: http://192.168.1.80/snapshot.jpg
    interval: 5s
    region: {x: 0.40, y: 0.35, w: 0.22, h: 0.10}
    engine: sevenseg
    trigger:
      type: numeric
      pattern: "([0-9.]+)"   # the reading, sign dropped; use "(-?[0-9.]+)" for a signed one
      op: gt
      threshold: 500
      confirm: 2
      cooldown: 1m
    notify:
      - ntfy://ntfy.sh/example-lab-scale

  # Fallback: skip reading entirely, just detect that the display changed.
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

A watch with no `engine:` line still uses tesseract; nothing changes for
your other watches. A sevenseg watch also starts on a box that has no
tesseract installed.

## Tuning notes

`pattern: "([0-9.]+)"` takes the digits and decimal point and ignores a
leading `-` and any `?`. That's deliberate: a `numeric` trigger with no
match simply doesn't fire that poll, so a `?` in the reading (a glyph the
decoder couldn't make a digit of) is skipped rather than parsed into a
wrong number. If you need the sign, use `"(-?[0-9.]+)"`. If a stray `?`
lands in the middle of a reading — `2?.5` — the pattern grabs `5` on its
own; a stricter pattern that pins the digit count you expect, say
`"^([0-9]{2,3}\\.[0-9])$"`, refuses such a poll outright.

`confirm: 2` is a reasonable default here where the printed-text recipes
go higher: the decoder is deterministic, so a display that isn't changing
produces the identical string poll after poll, and two matching polls are
enough to rule out a frame caught mid-refresh. A reading that changes every
poll (a scale settling) never reaches "stable" at any `confirm`, which is
the intended behaviour: it fires once the value holds.

Leave `preprocess` alone to start. The decoder binarizes the crop itself
and picks the polarity, so `invert` and `threshold` add nothing on a clean
display. Two of the sliders can still help: `upscale: 2` when the digits
are tiny in the frame (under ~40px tall), and `threshold` when the display
is so washed out — direct sunlight, an overexposed camera — that the
automatic split lands in the wrong place. Watch the crop preview: you want
crisp bars and a dark (or light) empty face, nothing in between.

## Caveats

- **Slant.** Many seven-segment fonts are italic, and a camera off to the
  side skews the digits further. The decoder expects upright bars; a mild
  slant reads fine, a strong one starts losing the side segments. Mount
  the camera as close to head-on as you can — for these displays a bar
  can go fully invisible off-axis, not just blurry.
- **Neighbouring marks.** A colon between two digit groups (a clock's
  `12:34`) is recognised and kept out of the digits, but degree signs,
  unit annunciators (`kg`, `°C`, `HOLD`) and the small "battery" glyphs
  many meters show inside the digit row are not, and the decoder returns
  `?` for some and ignores others. Crop what you can, and for a clock an
  `ocr_match` on `[0-9:]+` is steadier than a `numeric` watch.
- **Blank leading digits** are simply absent: a four-position display
  showing ` 23.5` reads `23.5`, and the unlit ghost segments in the empty
  position are ignored. A display that shows leading zeros reads them
  (`023.5`), which `numeric` parses fine.
- **Segment fonts vary.** `6`, `7` and `9` come with and without their
  optional bar and both spellings read; a `1` is placed at the right of
  its cell; a `4` with an open top reads as `4`. A font with unusual
  proportions — very fat bars, or digits wider than they are tall — is
  outside what the decoder expects and will show up as low confidence or
  `?` in the test panel.
- **Fired on what the pattern extracted.** As with every `numeric` watch,
  the value it compares is whatever the capture group produced, not what
  the display physically shows. Watch the reading strip against the real
  display for a while after any change to the region or pattern before
  trusting a threshold near the values you care about.

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

Two watches on the same camera and region, one pattern each — see "Why two
watches, not one pattern" below for why a single combined pattern is the
wrong call here, not just a style choice.

```yaml
watches:
  - name: printer-complete
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
      pattern: "(?i)print complete"
      confirm: 3        # require 3 consecutive identical readings first
      cooldown: 2h       # a print run is long; don't re-notify on flicker
    notify:
      - ntfy://ntfy.sh/example-bambu

  - name: printer-error
    source: http://192.168.1.55/snapshot.jpg
    interval: 10s
    region: {x: 0.20, y: 0.35, w: 0.60, h: 0.15}
    preprocess:
      grayscale: true
      threshold: 130
    trigger:
      type: ocr_match
      pattern: "(?i)error"
      confirm: 3        # same glare protection, independent of the watch above
      cooldown: 15m      # errors deserve a faster re-alert than completions
    notify:
      - ntfy://ntfy.sh/example-bambu
```

## Why two watches, not one pattern

An earlier draft of this recipe used a single watch with
`pattern: "(?i)print complete|error"`, reasoning that `ocr_match` is
edge-triggered and each alternative would be "its own edge." That reasoning
is wrong, and worth walking through because the failure mode is silent.

`ocr_match` fires when the current stable reading matches the pattern *and*
the previous stable reading didn't. Crucially, that check is
`re.MatchString(new) && !re.MatchString(prev)` — a single yes/no per
reading, with no memory of *which* alternative matched. Watch it play out
against the printer's screen:

1. Screen reads "Idle" — pattern doesn't match. No fire, nothing to report.
2. Screen reads "Print Complete" — matches (the `print complete` branch),
   previous reading didn't match → **fires**. Correct.
3. Screen later reads "ERROR: nozzle jam" — this also matches the combined
   pattern (the `error` branch). But the *previous* stable reading,
   "Print Complete", matched too. The edge condition sees matched → matched
   and does not fire.

The completion notification arrives; the error notification never does —
not because of a bug in your pattern, but because one `ocr_match` watch can
only ever detect the transition *into* "the pattern matches something," once,
until the reading exits the matching set entirely (goes back to "Idle" or
similar) and re-enters it later. A combined alternation makes that exit
much less likely, since almost every state you care about is now inside the
matching set.

Splitting into `printer-complete` and `printer-error` gives each condition
its own independent match/no-match state. The error watch's pattern never
matches "Idle", "Printing", or "Print Complete" — so it sits in "not
matching" through that entire sequence, and a genuine transition into
"ERROR: nozzle jam" is a real, visible edge for *that* watch, completely
unaffected by whatever the completion watch has already fired.

## Tuning notes

Both watches poll the same camera and region independently — that's two
HTTP GETs to the camera per `interval` instead of one, which is negligible
for a local snapshot URL but worth knowing if you're watching several
states on a slower or rate-limited camera.

`confirm: 3` on both matters more here than almost anywhere else in this
gallery: touchscreen photos are exactly the kind of filmed-screen shot most
prone to glare, camera-shake blur, and moiré from the screen's own refresh —
a single bad frame misreading text is a false positive you don't want.
Three consecutive identical reads before believing a transition filters
almost all of that out. Each watch tracks its own confirm count, so a run
of glare that confuses one doesn't affect the other.

`cooldown` is deliberately different between the two: 2h on
`printer-complete` because a finished print sits on the screen for a while
before anyone starts the next one, and this stops a printer that flickers
between "Print Complete" and a status screen (common on some firmware) from
re-firing every repaint. 15m on `printer-error` because an error is more
urgent — you'd rather risk an extra notification than sit through a long
cooldown while something is jammed.

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
- The two-watch split fixes the error-after-completion case above, but the
  same underlying rule applies to any pattern you write: if you ever
  broaden a single watch's pattern to match more than one state you care
  about telling apart, expect the same silent-miss failure once the screen
  moves between those states without leaving the matching set in between.
- If your printer *does* expose Moonraker or OctoPrint (most Klipper-based
  builds, and any printer you've flashed OctoPrint onto), skip this recipe
  entirely — poll their status APIs directly, or use their own
  notification plugins, which know the actual print state rather than
  guessing it from pixels.

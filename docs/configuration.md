# Configuration

Everything lives in one YAML file, `config.yaml` by default (`-config` picks another). You rarely need to edit it by hand: the web UI creates watches and saves every field on a watch's page. This page is the reference for when you do.

```yaml
# How many days of readings to keep (default 30; -1 keeps them forever).
history_days: 30

auth:                      # optional, see security.md
  username: admin
  password: change-me

mqtt:                      # optional, see home-assistant.md
  broker: tcp://homeassistant.local:1883
  username: watchglass
  password: <PASSWORD>

watches:
  - name: printer
    source: http://192.168.1.50/snapshot.jpg
    interval: 5s
    region: {x: 0.25, y: 0.45, w: 0.5, h: 0.1}
    trigger:
      type: ocr_match
      pattern: "(?i)print complete"
      confirm: 3
      cooldown: 30m
    notify:
      - ntfy://ntfy.sh/<TOPIC>
```

## Watch fields

| Field | Default | Meaning |
|---|---|---|
| `name` | required | Shown everywhere and used in the watch's URL, so no `/`, `?` or `#`. |
| `source` | required | Where frames come from. See [sources.md](sources.md). |
| `interval` | `5s` | Time between polls. At least `1s`. |
| `max_interval` | off | Adaptive polling: back off up to this while nothing changes ([sources.md](sources.md#adaptive-polling)). |
| `health_after` | `3` | Failed polls in a row (no frame, or the engine errored) before the watch counts as down and sends one alert. A frame that reads as blank is a normal poll. |
| `region` | required | The box to read, as fractions of the frame: `x` and `y` of the top-left corner, `w` and `h` its size, all 0 to 1. Drawing it in the UI fills these in. |
| `preprocess` | off | Adjustments before reading text (below). |
| `engine` | `tesseract` | How text is read: `tesseract`, `sevenseg` or `rapidocr` (below). |
| `trigger` | required | When the watch fires (below). |
| `notify` | none | Notification URLs, one per line. See [notifications.md](notifications.md). |
| `tls_insecure` | `false` | Accept a self-signed HTTPS certificate ([sources.md](sources.md#self-signed-certificates)). |
| `headers` | none | Extra request headers for an `http(s)` source, as `"Name: value"` strings. |
| `unit`, `device_class` | none | How a `numeric` watch's number is described to Home Assistant ([home-assistant.md](home-assistant.md#units-and-device-classes)). |
| `check_moves` | `true` | Notice when the picture around the box moves ([below](#when-the-camera-or-the-screen-moves)). `false` turns it off. |
| `move_alert` | `false` | Also send one alert to the `notify` URLs when it does ([below](#when-the-camera-or-the-screen-moves)). |

Durations are written like `30s`, `5m`, `2h` or `1h30m`.

## Triggers

| `type` | Fires when |
|---|---|
| `ocr_match` | the text read matches `pattern`, a regular expression, after it didn't |
| `ocr_changed` | the text changes to a new value and holds there |
| `numeric` | a number read from the text crosses `threshold`: above it with `op: gt`, below it with `op: lt` |
| `pixel_change` | at least `threshold` percent of the region's pixels changed since the previous poll |

The three text types fire on an edge: when the condition starts to hold, not on every poll while it holds. To fire again, the reading has to leave the condition and come back.

`pattern` is a [Go regular expression](https://pkg.go.dev/regexp/syntax). Plain words work, `(?i)` ignores case, and `a|b` matches either. For `numeric`, `pattern` is optional: without it the first number in the text is used; with a capture group, the group's text is used (`"FLOW\\s*([0-9.]+)"`).

**`confirm`** (default 3) is how many readings in a row must agree before watchglass believes them, because a filmed screen flickers and a single frame can be misread. For `ocr_match` and `numeric` it counts readings that meet the condition, so a value that wobbles while staying above the threshold still counts. For `ocr_changed` it counts identical readings. `pixel_change` doesn't use it. The Live panel shows the count as it builds ("1 of 3 readings in a row needed to fire").

Leaving takes more evidence than entering: once an `ocr_match` or `numeric` condition has been met, it clears only after twice `confirm` readings in a row that don't meet it, so a misread frame or two never sends the same alert again. A reading that says nothing counts on neither side: a `sevenseg` frame with nothing lit, or a `?` reading the watch skips (a blank line from tesseract or rapidocr does count, as the condition clearing). After 15 minutes of nothing but those (or twice the slowest poll interval, if that is longer), the watch starts fresh. A restart during that spell starts the 15 minutes over, so the watch still forgets, just later. After that, a condition that still holds when the display comes back is reported again once any cooldown ends.

**`cooldown`** delays a repeat alert rather than dropping it. A new state that holds through the cooldown still fires once, at the first reading after it ends; one that goes away before then never fires. `pixel_change` has no edge, so a change that persists fires again once per cooldown.

**Restarts don't repeat an alert.** Each watch keeps when it last fired and what it last settled on in the history database, so restarting watchglass, updating it, or pressing **Save & restart watch** doesn't send the same alert again, and a running cooldown still ends on time. What it settled on is only trusted if the watch read the screen within the last 15 minutes (or twice its slowest poll interval, if that is longer); after a longer break, a condition that holds when watchglass comes back is reported as new, once any cooldown that was running ends. An alert that was not sent (a wrong notify URL, or watchglass stopping before it went out) doesn't count: after you fix the URL and save, or on the next start, a condition that still holds is sent again. A camera that is down when watchglass restarts stays down without a second "down" alert, and the first reading after that sends the one "back up" alert. Unlike a fire, that holds even if the "down" alert didn't go out: it isn't sent again. The down state is kept by watch name, so if you point a down watch at another camera in `config.yaml` while watchglass is off, the watch shows the old camera's error until its first reading, and that reading sends a "back up" alert.

A watch starts fresh, and fires once if its condition already holds, when you change what it looks at or looks for: `source` (a new camera password in the URL doesn't count), `region`, `engine` and `preprocess` (`pixel_change` reads neither), or the trigger's `type`, and its `pattern`, `op` or `threshold` where the type uses them. Changing `notify`, `interval`, `confirm`, `cooldown` or anything else keeps what it knew. Renaming a watch makes it a new one, and deleting a watch deletes what it knew.

A new watch on the UI shows three presets above the trigger fields, which fill them in without saving: **Status text** (`ocr_match` on `(?i)complete|done|error`), **Digit display** (`sevenseg` and `numeric`) and **Any change** (`pixel_change` at 20%).

### Picking a pixel_change threshold

A camera never sends exactly the same picture twice. Sensor noise, JPEG compression, a flickering backlight and a mount that shakes all change some pixels when nothing on the screen does. The threshold has to sit above that noise and below the change you want to hear about, so measure both on your own camera:

1. Draw the box and press **Test this region** with the screen left alone. For `pixel_change` it grabs two frames 1 to 3 seconds apart (the watch's interval, kept in that range) and says how much of the region changed, compared the way the running watch compares each frame with the one before. That's a first look at the noise, from one pair of frames.
2. Save the watch with Threshold `100` and its notify list empty while you measure, and leave the screen alone. At 100, noise alone won't fire it, but a frame where every pixel in the box changes at once (the lights go out, the camera restarts or changes its picture size) still fires, and with nothing to notify that sends no alert. Each tile in the Live panel says how much changed since the frame before ("4.2% changed"), and the panel keeps the last 10 readings. Look at it now and then over a few minutes, or longer if the room's light changes (lamps, daylight, a camera that switches to night vision), and note the highest number you see. That's your camera's noise.
3. Press Test while the screen does what you want to hear about (a light comes on, a page changes, a number moves on), or watch the Live panel as it happens. That's the smallest real change.
4. Set the threshold between the two: above the highest noise you saw, and below the real change. Then put the watch's notify targets back. If the real change isn't clearly bigger than the noise, no threshold will work; see below.

`confirm` doesn't apply to `pixel_change`: one pair of frames over the threshold fires. `cooldown` stops a run of noisy frames from sending an alert each, but it also hides a real change that comes during it.

On an emulated camera with a shake of 1 to 3 pixels, a flickering backlight, passing hands and an IR switch, the frames with nothing changing moved 9% of the box half the time, 22 to 29% once in a hundred frames and up to 99% when a hand passed or the IR light switched. The real change, a counter going from 984 to 1284, moved only 6 to 22%. A threshold of 5% sent 112 false alerts an hour, and with a 30 s cooldown only 4 of 30 real changes got their own alert. 40% sent 8 false alerts and missed all 30 real changes. The same camera held to a shake of at most 1 pixel, with no flicker, moved at most 4.4% when still, and the change always moved more than 5%, so 5% separated them.

So a camera that shakes by more than a pixel, or whose picture flickers (auto exposure hunting, an IR light switching on and off, a backlight beating with the camera's frame rate), may have no threshold that works. Then:

- Mount the camera so it can't move: a bracket, not a shelf that a door or a fan shakes.
- Steady the light: fixed exposure if the camera has it, IR or night mode off, a lamp that stays on.
- Draw a smaller box around only the part that changes (a status light rather than the whole panel), so the change is a bigger share of the box.
- Use a text watch if what changes is words or digits. `ocr_changed` or `ocr_match` with `confirm` ignores a picture that moves while the text stays the same.

## Preprocess

These only affect reading text; `pixel_change` always compares the raw crop. Tune them with **Test this region**, which shows the crop after preprocessing next to what was read.

```yaml
preprocess:
  rotate: 90        # 90, 180 or 270: turn the crop this many degrees clockwise; 0 is off
  grayscale: true
  invert: false     # light text on a dark screen often reads better inverted
  threshold: 128    # 1-255: turn every pixel black or white at this level; 0 is off
  upscale: 2        # 2-4: enlarge small text; 0 or 1 is off
```

`rotate` runs first, then `upscale`, then `grayscale`, `invert` and `threshold`.

### A display that is sideways

A phone on its side, an ESP32-CAM screwed in at a right angle, a meter whose LCD faces the wrong way: tesseract, `sevenseg` and rapidocr all need the text upright. `rotate` turns the crop a quarter or half turn before it is read: `90` when the tops of the letters point left in the picture, `270` when they point right, `180` when the display is upside down. In the UI it is **Rotate**, the first control under Preprocess. Try a value and press **Test this region**: the result shows the turned crop.

The region doesn't change. It stays a box on the picture as the camera sends it, and the page keeps showing that picture unturned; only the crop is turned. The crops in the Live panel, the picture attached to an ntfy alert and Home Assistant's snapshot are the turned crop too, so they arrive the right way up.

`rotate` takes those four values and nothing in between. A display that leans a few degrees is a different problem: see the `ffmpeg:` source trick in the [seven_segments recipe](recipes/from-seven-segments.md#setting-by-setting), or straighten the camera.

## OCR engines

| `engine` | Reads | Needs |
|---|---|---|
| `tesseract` (default) | printed text: LCD menus, console output, status lines | the tesseract program ([install.md](install.md#what-to-install-for-reading-text-and-streams)) |
| `sevenseg` | seven-segment digits: scales, meters, thermostats, appliance timers | nothing, it's built in |
| `rapidocr` | printed text tesseract struggles with: low contrast, small or stylised fonts, odd angles, several lines at once; sometimes a [digit display `sevenseg` can't read](#rapidocr-on-digit-displays) | Python with `pip install rapidocr onnxruntime`; not in the Docker image |

tesseract reads the region as **one line of text**. Draw the box around a single line; two watches on the same camera are cheap if you need two lines. rapidocr reads a block of lines, joined with spaces.

A watch that needs an engine this machine doesn't have doesn't start, and its page says what to install. A `sevenseg` watch runs on a machine with no tesseract at all.

### How `sevenseg` reads digits

It's a geometry decoder, not a font model. It works out the polarity itself, so lit LEDs on a dark face and dark LCD digits on a light one both read without any `preprocess`. It finds each digit's bars, checks the seven segment positions, and returns the digits joined: `23.5`, `-8.0`, `1234`, and `1:23` for a clock or timer. A glyph whose bars spell no digit (a letter, a half-lit segment) comes back as `?`, so `E4` on a boiler reads `?4`. So does a digit read below 40% confidence that could be another digit: a 0 whose middle bar is nearly lit might be an 8, and the reading says `?` rather than guess. A `numeric` watch skips every reading with a `?` in it, since the number around it may be wrong (`?5.5` may be 15.5): the reading doesn't count towards `confirm`, doesn't change the watch's state and doesn't reach Home Assistant. A frame with no digits on it at all (a display caught dark between refreshes, or behind a glare) is skipped the same way, so it doesn't start the count over. An `ocr_match` pattern sees the `?` as text and can match it on purpose; a reading with a `?` that the pattern doesn't match is skipped like a blank frame. The `?` of a digit read below 40% never matches, though: that reading is skipped too, so a 52 with its 5 half lit by glare, read `?2`, isn't taken for a fault code. `ocr_changed` takes a reading with a `?` that fits the last stable text (same length, every digit it did read the same: `?2.5` after `22.5`) as that text read partly, not as a change; one that doesn't fit (`?` after `0:04`, or a blank frame) is still a change, so a washer's End and a display that went dark still fire. The other way round, a whole reading that fits a partly read text the watch settled on (`22.5` after `?2.5`) takes its place without firing, so a change the camera first caught partly fires once, not again when it reads whole. A change that only ever reads with a `?` in the same place (`12:3?` after `12:34`) isn't reported until a frame reads it whole. A `?` reading the watch skips, or takes as its text read partly, doesn't reach Home Assistant's Reading either; one the pattern catches on purpose does.

**Test this region** shows one chip per glyph with its confidence; below 60 it's shown in red, and that's the digit to worry about. When the reading shows a `?` where the chip shows a digit, the decoder saw that digit below 40% and it could have been another one, so the watch doesn't act on it. Draw the box around the digits with a little room (leave out units and labels; some of the bezel or housing in the box is fine, and Test shows if it isn't) and keep the camera close to head-on: the decoder expects upright digits and copes with a mild slant, not a strong one, and not a display turned on its side. The digits need to be about 30 pixels tall in the camera's picture. [The lab instrument recipe](recipes/lab-instrument-seven-segment.md) covers what still trips it up.

### rapidocr

rapidocr runs the PaddleOCR PP-OCR models through the [RapidOCR](https://github.com/RapidAI/RapidOCR) Python package. watchglass doesn't link it: each read starts Python, hands it the crop and reads the text back, so the binary stays small and static and the engine stays optional. `pip install rapidocr` alone isn't enough; `onnxruntime` is what runs the models. Each read takes about 4 s on a desktop PC, up to 6, and more on a Pi, so give these watches an `interval` of 10 s or more.

watchglass runs rapidocr with its text-direction check off. That check is meant for photographed pages that may be upside down, and it turns digit displays over: with it on, 14 of the 73 digit displays below had some or all of their digits read upside down, with full confidence (`12:05` as `50:21`, `8.34` as `hE8`). With it off, rapidocr no longer turns text that really is upside down. If the display is upside down in the camera's picture (a camera mounted the wrong way up), set `rotate: 180` ([a display that is sideways](#a-display-that-is-sideways)); Test this region shows the turned crop. A rapidocr watch on such a display that read fine on 0.1.12 or earlier needs it now.

#### rapidocr on digit displays

`sevenseg` is the engine for seven-segment digits, and rapidocr is worth a try only on a display `sevenseg` can't read. On 73 photos of real digit displays (clocks, meters, thermostats, pumps, VFDs, scales):

| | read exactly | confidently wrong | unsure or nothing |
|---|---|---|---|
| `sevenseg` | 37 | 2 | 34 |
| rapidocr | 46 | 13 | 14 |

- On the 34 displays `sevenseg` couldn't read, rapidocr read 18 exactly and gave a confident wrong reading on 5.
- On the 37 that `sevenseg` read, rapidocr got 6 wrong, so don't switch a display that already reads.
- rapidocr's wrong readings look like readings: `3 14 g` for 314 on a kitchen scale, `9.86 96 044` for 9.8696044 on a calculator. A `numeric` watch takes the first number, so it would act on 3 or 9.86.
- A rapidocr read takes seconds (above); `sevenseg` takes a few hundredths of a second.

So if Test this region gets only `?` or no digits from `sevenseg` whatever you try, switch the Engine to rapidocr and press Test on several frames, including the display's other states. Keep it only if every one reads right, and leave the watch's `confirm` at 3 or more.

## When the camera or the screen moves

A camera gets bumped, a screen slides on its stand, or the camera's switch to night vision shifts the picture. The box then sits partly off the digits, and the watch reads part of the number or the wrong one. watchglass notices, and says so on the watch's page.

**How.** After a watch's first three clean readings (text with no unsure digit, and for `sevenseg` no `?`; any frame for `pixel_change`), it keeps a small grayscale picture of the area around the box: the bezel, the housing, the labels. The box itself is left out, so digits that change don't count. Every 5 seconds it lines the current frame up with that picture by the edges in it, so a change of exposure, white balance or night vision doesn't count either, and works out how far the picture has moved. It also tries small turns, up to about 4 degrees, so a camera that was knocked a little crooked is measured at the box rather than across the whole picture.

**What it says.**

- When four checks in a row (about 20 seconds) agree that the picture moved by at least 4.5 pixels, the Live panel says "The picture around this box moved about 14 px right and 3 px down since 14:02. Readings may be wrong until the box is moved." The watch list marks the watch "camera moved", and the log has a line. A camera that shakes on its own has to move by more: about 3 times how far its picture usually jumps from one check to the next, if that is more than 4.5 pixels, and never more than 9. A picture that creeps, or gets nudged a few pixels at a time, is reported once it has gone that far in all. While the warning is shown, a further move updates the offset on the page and in the log, without a new alert.
- When nothing lines up for 5 minutes, and the box hasn't read cleanly in that time (three clean readings in a row; a `?` from the seven-segment decoder isn't a clean reading, and nor is a lone digit it makes out in the dark), the panel says the picture around the box hasn't been in view since then: the lens is covered, or the camera points somewhere else. A screen that shows something else for a while (a menu, a boot screen) while the box still reads isn't reported.
- When the picture moves back, the warning goes away by itself.

**What to do.** The warning has two buttons:

- **Move the box with it** moves the box by the measured amount, saves it to `config.yaml` (comments kept, as every save), restarts the watch and takes a new picture of the area around the box. Moving the box is a change of `region`, so the watch starts fresh, as it does for any region change ([triggers](#triggers)).
- **The new position is fine** keeps the box where it is and takes a new picture from the next frame. Press it once the camera shows what it should: a picture of a covered lens is too plain to go by, and the check then stops until the watch is saved.

Changing the box or the `source` and saving takes a new picture too.

**The alert.** With `move_alert: true`, a move (or a picture that can't be found) also sends one alert to the watch's `notify` URLs, titled `watchglass: <name> (camera moved)` or `watchglass: <name> (box not in view)`, with the same retries as other alerts. It goes out once per move: not again while the picture stays moved, and not again after a restart. It is off by default because it is a new kind of message that a watch set up before it didn't ask for.

**What it can't notice.** A move smaller than its threshold. A move bigger than the margin it keeps around the box (60% of the box's longer side), which reads as "can't find" instead. Zoom. A box with too little around it to go by: one that fills nearly the whole frame, or sits on a plain wall, gets a note on its page that the area around it is too plain or changes too much, and isn't checked until it is saved again or watchglass restarts. The same goes for a watch that never reads cleanly, since it never takes the picture. On a screen grab or a KVM, where nothing around the box stays put, set `check_moves: false`. Do the same for a box on a screen whose text scrolls around it (a console, a log, a list): when the lines move up, the check can't tell that from the camera moving, and says the picture moved by a line or more.

**Restarts.** The picture, and what the check last said, is kept in the history database, one small row per watch. A camera bumped while watchglass was off still shows as moved after the restart, and the same move isn't reported twice.

**Accuracy and cost.** On the 77 freely licensed photos of round 6's test set, moved by 5, 10 and 20 pixels, the measured offset was within 2 pixels of the true one on 460 of 462, and on 148 of 154 turned by 2 degrees. Changes of exposure (0.65 to 1.45 times) and white balance gave no shift of more than about a pixel. Two replays of the first hour of round 6's 73-minute recording of emulated cameras (12 watches polling every 1 to 2 seconds, with shake, exposure steps, night vision, hands in front, a camera reboot and a restart of watchglass) reported no move and no lost picture. The checks measured in that recording, run through the move rule in every stretch of 150 checks, forwards and backwards (316,272 checks in all), gave no move either. A check takes about 5 to 25 milliseconds on a desktop PC; most polls skip it. For those 12 watches it added about 2% of one CPU core, compared with the same build without it on the same frames. The picture it keeps takes at most 37 KB per watch. Each check makes 3 to 4 MB of short-lived garbage, so the process's memory sat about 1 MB higher most of the time, with peaks 15 to 30 MB higher before Go's garbage collector caught up.

## History

Every reading goes into a SQLite database (`-db`, default `watchglass.db`). `history_days` sets how long readings are kept: 30 days by default, or `-1` to keep everything. Old readings are pruned at start-up and then once a day. The same file keeps one small row per watch with what its trigger knows ([restarts](#triggers)); pruning leaves those alone, and a watch's row goes when the watch is deleted.

For sizing: a reading takes about 70 bytes with its indexes when the watch name and the text read are short (a dozen characters each; longer ones take more), so a watch polling every 2 s writes about 3 MB a day, roughly 90 MB over the default 30 days. That scales with the interval (poll half as often, half the size) and with the number of watches, which matters on an SD card.

## Saving from the web UI keeps your file

**Save** merges the change into `config.yaml` instead of rewriting it. Comments, key order, quoting, anchors, line endings and `- ` placement survive, and a save that changes nothing doesn't touch the file. What the first real change can lose: blank lines may be collapsed, end-of-line comments lose their alignment, a comment at the end of a block may shift its indentation, nested blocks in a 4-space file are re-indented, and a URL inside a flow-style list (`[...]`) comes back quoted. Deleting a watch deletes its own comments; a comment block parked above it moves to the next watch. A file that doesn't parse (a hand edit left half done) is never overwritten: Save reports the error and leaves it alone.

Save also checks what it writes: a notification URL that can't work, a unit Home Assistant won't take, or an engine that isn't installed is refused with the reason, and nothing is written. Hand edits to `config.yaml` take effect when watchglass restarts.

[examples/watchglass.yaml](../examples/watchglass.yaml) is a commented example with one watch of each kind.

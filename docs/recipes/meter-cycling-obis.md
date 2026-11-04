# Meter that cycles through registers

Many electricity meters don't show one total. They step through their registers every few seconds: the import total with the code `1.8.0` beside it, the export total with `2.8.0`, sometimes tariff 1 and tariff 2 (`1.8.1`, `1.8.2`), often with a blank or all-segments frame between. The totals look alike, so a pattern on the number can't tell them apart, and two watches would read two different frames.

A `gate` reads the code and the total from the same frame. The watch below publishes the import total and skips every frame where the code box shows anything else.

This recipe is tested on three drawn frames of such a meter: [1.8.0](samples/meter-obis-1-8-0.jpg) at 004521.7 kWh, [2.8.0](samples/meter-obis-2-8-0.jpg) at 001208.3 kWh, and a [blank](samples/meter-obis-blank.jpg) one between them, and on a real photo of a Logarex meter on 1.8.0 ([below](#on-a-real-meter)). It was also run against three real photos of an Itron meter cycling between its delivered (`2`) and received (`12`) registers and its segment test, with blank frames between them made by erasing the digits from one of the photos: for ten minutes, the published value was only ever the delivered total.

## The watch

```yaml
watches:
  - name: meter-import
    source: http://192.168.1.70/snapshot.jpg
    interval: 2s
    engine: sevenseg
    # The total, digits only.
    region: {x: 0.31, y: 0.35, w: 0.56, h: 0.22}
    # The register code beside it. Read the total only while it shows 1.8.0.
    gate:
      region: {x: 0.15, y: 0.35, w: 0.15, h: 0.12}
      pattern: '^1\.?8\.?0$'
    unit: kWh
    device_class: energy
    max_rise: 5
    trigger:
      type: value
      confirm: 2
```

<!-- sample-check: meter-import samples/meter-obis-1-8-0.jpg met reads=004521.7 gate=open -->
<!-- sample-check: meter-import samples/meter-obis-2-8-0.jpg skipped gate=closed -->
<!-- sample-check: meter-import samples/meter-obis-blank.jpg skipped gate=closed -->

On the 1.8.0 frame, **Test this region** says "Gate open" with the gate box reading `1.8.0`, and reads the value `004521.7`. On the 2.8.0 frame it says "Gate closed": the gate box reads `2.8.0`, so the watch skips that frame, and the value box below it is only for reference. On the blank frame the gate box reads `?` and the frame is skipped too; a `?` isn't a code, so the watch page goes on naming the last code it read.

- `gate.pattern: '^1\.?8\.?0$'`: `^` and `$` make it the whole reading, so `1.8.1` (tariff 1) doesn't open the gate. The dots are escaped (`\.`) and each may be missing (`?`): the seven-segment decoder can miss a small dot, on a real meter more often the smaller the code is in the frame, and then reads `1.8.0` as `1.80` or `180`. With `'^1\.8\.0$'` such a gate never opens. For the export total, make a second watch with `'^2\.?8\.?0$'` on the same boxes.
- Draw the gate box with room around the code. A box that clips the last digit can turn `1.8.0` into `1.8.1`, and then the gate never opens.
- `interval: 2s`: the watch only sees 1.8.0 when a poll lands on it. If your meter shows each register for 5 seconds or more, 2 seconds catches each one at least once a cycle. Leave `max_interval` off: on a meter that keeps cycling, a gated watch polls at `interval` anyway.
- `confirm: 2`: the value is the middle of the last three totals read while the gate was open. Skipped frames don't count, so the readings in between don't slow it down or reset it.
- `device_class: energy` makes it a counter for the Energy dashboard, and `max_rise: 5` holds a misread top digit back; see [Electricity or water meter into the Energy dashboard](meter-energy-dashboard.md).

## On a real meter

The drawn frames are clean. A real meter's LCD has glare, a grey background and small dots. Here is the same watch on a [photo of a Logarex meter](samples/meter-logarex.jpg) showing `1.8.0` beside `000000` kWh, with its boxes moved to fit, and a second one for the export total:

```yaml
watches:
  - name: logarex-import
    source: http://192.168.1.71/snapshot.jpg
    interval: 2s
    engine: sevenseg
    region: {x: 0.24, y: 0.34, w: 0.49, h: 0.30}
    gate:
      region: {x: 0.08, y: 0.45, w: 0.14, h: 0.17}
      pattern: '^1\.?8\.?0$'
    unit: kWh
    device_class: energy
    max_rise: 5
    trigger:
      type: value
      confirm: 2
  - name: logarex-export
    source: http://192.168.1.71/snapshot.jpg
    interval: 2s
    engine: sevenseg
    region: {x: 0.24, y: 0.34, w: 0.49, h: 0.30}
    gate:
      region: {x: 0.08, y: 0.45, w: 0.14, h: 0.17}
      pattern: '^2\.?8\.?0$'
    unit: kWh
    device_class: energy
    trigger:
      type: value
      confirm: 2
```

<!-- sample-check: logarex-import samples/meter-logarex.jpg met reads=000000 gate=open -->
<!-- sample-check: logarex-export samples/meter-logarex.jpg skipped gate=closed -->
<!-- sample-check: logarex-import samples/meter-logarex-small.jpg met reads=000000 gate=open -->
<!-- sample-check: logarex-export samples/meter-logarex-small.jpg skipped gate=closed -->

On the photo, **Test this region** says "Gate open" for `logarex-import` and reads `000000`, and "Gate closed" for `logarex-export`. Draw the gate box a little past the code on every side. How big the code is in the frame matters more than the exact box. Here is what 144 boxes around the code read on this photo, and on the same photo scaled down:

- At full size, with the code about 24 pixels high, nearly every box read `1.8.0`.
- At three quarters, a quarter of the boxes read `18.0`.
- At 0.6, with the code about 14 pixels high, no box read `1.8.0`. Two thirds read `1.80` or `180`, and only the pattern with optional dots opens on those. This [smaller copy](samples/meter-logarex-small.jpg) is tested too: its gate reads `180`, and the watch above still reads `000000`.
- At half size, the decoder couldn't read the code at all, and the gate stays closed. Move the camera closer or zoom in.

## On the watch page

Tick **Use a gate** under the trigger, enter the pattern, and draw the gate box: it is dashed and blue on the snapshot, next to the value's solid box. **Value box** and **Gate box** under the image pick which one the drag, the arrow keys and **Set region manually** move; a click inside a box picks it too, and a drag always draws the box that is picked, even one that starts inside the other. **Test this region** reads both from one frame.

The Live panel shows only the readings from frames where the gate was open, and a line under the reading says what the gate did on the last frame. If the gate stays closed for 5 minutes, the line says "Gate closed for 6 min (last saw '2.8.0')" and the watch list tags the watch, also when the gate hasn't opened once and there is no reading yet. The watch doesn't count as down, and Home Assistant shows it online: the camera works and the gate was read. If the display should be showing 1.8.0 by then, check the gate box and the pattern.

## What can still go wrong

- A register that shows for less than one poll interval may never be seen. Shorten `interval`, or ask the meter's menu for a longer display time.
- A code read wrong in a way that still matches the pattern (`1.8.0` misread as itself while the meter shows `1.6.0`) lets another register's total through. Anchored patterns and `confirm` keep this rare; `max_rise` on a counter catches a jump to a much bigger total.
- Some meters mark the register with an arrow or a tariff symbol rather than digits. The gate reads text and digits only, so a symbol the engine can't read can't be gated on.

# Meter that cycles through registers

Many electricity meters don't show one total. They step through their registers every few seconds: the import total with the code `1.8.0` beside it, the export total with `2.8.0`, sometimes tariff 1 and tariff 2 (`1.8.1`, `1.8.2`), often with a blank or all-segments frame between. The totals look alike, so a pattern on the number can't tell them apart, and two watches would read two different frames.

A `gate` reads the code and the total from the same frame. The watch below publishes the import total and skips every frame where the code box shows anything else.

This recipe is tested on three drawn frames of such a meter: [1.8.0](samples/meter-obis-1-8-0.jpg) at 004521.7 kWh, [2.8.0](samples/meter-obis-2-8-0.jpg) at 001208.3 kWh, and a [blank](samples/meter-obis-blank.jpg) one between them. It was also run against three real photos of an Itron meter cycling between its delivered (`2`) and received (`12`) registers and its segment test, with blank frames between them made by erasing the digits from one of the photos: for ten minutes, the published value was only ever the delivered total.

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
      pattern: '^1\.8\.0$'
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

On the 1.8.0 frame, **Test this region** says "Gate open" with the gate box reading `1.8.0`, and reads the value `004521.7`. On the 2.8.0 frame it says "Gate closed": the gate box reads `2.8.0`, so the watch skips that frame, and the value box below it is only for reference. On the blank frame the gate box reads `?` and the frame is skipped too.

- `gate.pattern: '^1\.8\.0$'`: the dots are escaped, and `^` and `$` make it the whole reading, so `1.8.1` (tariff 1) doesn't open the gate. For the export total, make a second watch with `'^2\.8\.0$'` on the same boxes.
- Draw the gate box with room around the code. A box that clips the last digit can turn `1.8.0` into `1.8.1`, and then the gate never opens.
- `interval: 2s`: the watch only sees 1.8.0 when a poll lands on it. If your meter shows each register for 5 seconds or more, 2 seconds catches each one at least once a cycle. Leave `max_interval` off: on a meter that keeps cycling, a gated watch polls at `interval` anyway.
- `confirm: 2`: the value is the middle of the last three totals read while the gate was open. Skipped frames don't count, so the readings in between don't slow it down or reset it.
- `device_class: energy` makes it a counter for the Energy dashboard, and `max_rise: 5` holds a misread top digit back; see [Electricity or water meter into the Energy dashboard](meter-energy-dashboard.md).

## On the watch page

Tick **Use a gate** under the trigger, enter the pattern, and draw the gate box: it is dashed and blue on the snapshot, next to the value's solid box. **Value box** and **Gate box** under the image pick which one the drag, the arrow keys and **Set region manually** move; a click inside a box picks it too, and a drag always draws the box that is picked, even one that starts inside the other. **Test this region** reads both from one frame.

The Live panel shows only the readings from frames where the gate was open, and a line under the reading says what the gate did on the last frame. If the gate stays closed for 5 minutes, the line says "Gate closed for 6 min (last saw '2.8.0')" and the watch list tags the watch, also when the gate hasn't opened once and there is no reading yet. The watch doesn't count as down, and Home Assistant shows it online: the camera works and the gate was read. If the display should be showing 1.8.0 by then, check the gate box and the pattern.

## What can still go wrong

- A register that shows for less than one poll interval may never be seen. Shorten `interval`, or ask the meter's menu for a longer display time.
- A code read wrong in a way that still matches the pattern (`1.8.0` misread as itself while the meter shows `1.6.0`) lets another register's total through. Anchored patterns and `confirm` keep this rare; `max_rise` on a counter catches a jump to a much bigger total.
- Some meters mark the register with an arrow or a tariff symbol rather than digits. The gate reads text and digits only, so a symbol the engine can't read can't be gated on.

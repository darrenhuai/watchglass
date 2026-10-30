# Electricity or water meter into the Energy dashboard

Most electricity, water and gas meters show their total on a seven-segment LCD and nothing else: no pulse output you can reach, no optical port, no API. A camera on the display and a `value` watch turn that total into a Home Assistant sensor the Energy dashboard can use, and a CSV of readings if you don't run Home Assistant at all.

This recipe is tested on a real photo of a Logarex household meter ([sample](samples/meter-logarex.jpg)), which reads `000000` kWh.

## Before you trust it: reflective LCDs

A meter's LCD has no backlight. It reflects whatever light falls on it, so a camera sees glare, shadows, and digits with less contrast than an LED display. The seven-segment decoder reads these less reliably than lit digits: on many photos of real meters it reads a digit as `?`, and now and then it reads a digit wrong. A `?` is harmless (the reading is skipped and the value stays as it was); a wrong digit is what the rest of this recipe guards against.

Check before you put the number on a dashboard:

1. Draw the box around the digits only and press **Test this region** a few times through the day: morning sun, evening lamp, a cloudy noon. Every chip should be green. A red chip, or a `?` in the reading, is a digit the decoder isn't sure of.
2. Light the meter yourself if you can. A small LED lamp at an angle, always on, does more for a reflective LCD than any setting.
3. After a day, compare the watch's value with the meter itself, and look at **Download readings (CSV)** on the watch page: the `reading` column shows every frame as read, so a run of `?` or a gap says when the camera had trouble.

## The watch

```yaml
watches:
  - name: house-meter
    source: http://192.168.1.70/snapshot.jpg
    interval: 30s
    region: {x: 0.24, y: 0.34, w: 0.49, h: 0.30}
    engine: sevenseg
    unit: kWh
    device_class: energy
    max_rise: 5
    trigger:
      type: value
      confirm: 3
```

<!-- sample-check: house-meter samples/meter-logarex.jpg met reads=000000 -->

On the sample, **Test this region** reads `000000` and says the value is 0.

- `type: value` records the number and never sends an alert. If you also want an alert, say above a daily figure, add a second watch with `type: numeric` on the same box.
- `device_class: energy` with `unit: kWh` makes the watch a counter and its Home Assistant sensor `total_increasing`, which the Energy dashboard takes. For a water meter use `device_class: water` and `unit: m³` (or `L`); for gas, `device_class: gas`.
- `confirm: 3`: the value is the middle of the last five numbers read, so one or two misread frames never show.
- `max_rise: 5`: the most the total can go up between two readings, in kWh. A house uses a few kWh an hour at most, so a rise of more than 5 in 30 seconds is a misread digit, not energy. It is held until five readings in a row agree on it, so a real jump after the camera was off still gets through. Pick a number well above the most your meter moves between two polls.
- Leave out the meter's other fields: the `1.8.0` register name, the `kWh` label and the arrows. A meter that cycles between registers (total, tariff 1, tariff 2) needs a gate on the register's code: see [Meter that cycles through registers](meter-cycling-obis.md).

## Home Assistant

With an `mqtt:` block in `config.yaml` ([home-assistant.md](../home-assistant.md)), the watch appears as the device "watchglass house-meter" with a Value sensor in kWh. Then:

1. Open the Energy settings (**Settings → Dashboards → Energy**), press **Add grid connection** under **Electricity grid**, and pick "watchglass house-meter Value".
2. HA builds statistics every 5 minutes; new data can take up to 2 hours to show on the dashboard.

A counter never goes down: a lower reading is held back, and the watch page says so ("Held back: 4790 kWh is lower than the last value 4815 kWh"). If the display shows the right total again, nothing needs doing. **If the meter was replaced**, the new one starts low: press **Accept** under the held value, and the counter carries on from there. Home Assistant takes the drop as a new meter and keeps adding to the dashboard.

## Without Home Assistant

Every reading is in the history database with the value it published. **Download readings (CSV)** on the watch page, or `/watch/house-meter/history.csv?from=2026-10-01`, gives time, reading, value and fired, one row per reading, for a spreadsheet or a script. See [configuration.md](../configuration.md#readings-out-as-csv).

## What can still go wrong

- A digit read wrong for longer than the checks allow: five readings in a row that agree on the same wrong total. Raise `confirm`, or light the display better.
- A meter that scrolls between screens (total, current power, date) shows other numbers in the same box. Most can be set to stay on the total; if yours can't, `pattern` can only help when the screens look different, and a `?` from a screen with letters is skipped.
- A total with more digits than the box: when the meter rolls over to a new digit on the left, widen the box.

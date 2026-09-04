# Recipe gallery

Known-good `watches:` blocks for specific hardware. Each recipe states the
problem, gives you a complete config to copy-paste, and explains how to
tune it — with honest notes on where it can still fail. Start with the
generic camera recipe if you're not sure which one applies to you.

- [Generic snapshot camera](generic-snapshot-camera.md) — any camera with a
  plain HTTP snapshot URL; the starting point for everything else here.
- [RTSP camera](rtsp-camera.md) — PoE/NVR cameras that only speak RTSP, the
  ffmpeg requirement, and a go2rtc fallback for cameras RTSP can't reach.
- [Closed-firmware printer LCD](printer-lcd.md) — OCR a printer's
  touchscreen when it has no local API (e.g. Bambu Lab in LAN-only mode);
  use OctoPrint or Moonraker instead if you have one.
- [Server console / IPMI KVM](server-console-ipmi.md) — a numeric
  temperature trigger read off a remote console, tuned for KVM links that
  drop the occasional frame.
- [Lab instrument seven-segment readout](lab-instrument-seven-segment.md) —
  the preprocessing walkthrough for LED/LCD digit displays, and an honest
  look at where general-purpose OCR still struggles with that font.

## Don't see your screen?

These five cover the devices this project started with — they're not the
limit. What screen in your house or lab has no API?
[Open an issue](https://github.com/darrenhuai/watchglass/issues) describing
the device and how you're capturing it (camera model, snapshot URL shape,
capture card, whatever you've got), and we'll help turn it into a recipe
and add it here.

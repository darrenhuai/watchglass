# Recipes

Configs for specific screens that you can copy and adjust. Each one says what it's for, gives a complete `watches:` block, and is honest about where it can still go wrong. The newer ones are tested against a [sample frame](samples/) in this repository: `go test ./internal/docscheck` reads the frame with the recipe's own watch and checks the result.

## Start here

- [Camera URL cookbook](camera-urls.md): the snapshot or stream URL for Reolink, Hikvision, Dahua, Tapo, Wyze, UniFi, ESP32-CAM, ESPHome, phones, OctoPrint, Frigate, go2rtc, Home Assistant cameras, KVMs, and your own screen.
- [Generic snapshot camera](generic-snapshot-camera.md): the basic shape every other recipe builds on.

## By screen

- [Heat pump or boiler panel](heat-pump-boiler-panel.md): a temperature as a Home Assistant sensor, and fault codes like `E04` to your phone, on a text LCD or a seven-segment display. Tested on 4 frames.
- [Appliance time remaining](appliance-time-remaining.md): a washer's `0:04` countdown, "5 minutes left", and pairing it with a smart plug. Tested on 4 frames.
- [Moving over from seven_segments / ssocr](from-seven-segments.md): Home Assistant's seven_segments settings mapped to a watch, side by side. Tested on 1 frame.
- [Server console, IPMI and KVMs](server-console-ipmi.md): kernel panics, GRUB rescue and "No bootable device" through PiKVM, NanoKVM, JetKVM or a capture card, plus a temperature read off a console. Tested on 2 frames.
- [Lab instrument seven-segment readout](lab-instrument-seven-segment.md): bench scales, multimeters and thermometers on the built-in decoder, and what still trips it up.
- [Closed-firmware printer LCD](printer-lcd.md): a printer's touchscreen when it has no local API. If it has OctoPrint, Moonraker or a vendor API, use that instead.
- [RTSP camera](rtsp-camera.md): cameras that only speak RTSP, and go2rtc for the ones watchglass can't reach.

## Don't see your screen?

[Ask for a recipe](https://github.com/darrenhuai/watchglass/issues/new?template=recipe.yml). The form asks for the device, what's on the screen, how you capture it and what should trigger, and for a frame of it. A frame of the real device is what turns a guess into a tested recipe. [CONTRIBUTING.md](../../CONTRIBUTING.md#add-or-change-a-recipe) explains how to write one yourself.

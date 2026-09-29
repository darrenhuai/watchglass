# Recipe sample frames

The frames the recipes are tested against. Each was drawn with Pillow for this repo: the panels, digits and text are synthetic, not photos of real devices. The camera-view ones have a little blur, sensor noise and vignetting so they read like what a cheap camera sends, and are JPEGs, as most cameras send. The console screens are 720x400 PNGs, like a KVM's capture of a text console.

| Frame | Shows | Used by |
|---|---|---|
| [seven-segment-counter.jpg](seven-segment-counter.jpg) | a red LED counter reading 1284 | [from-seven-segments.md](../from-seven-segments.md) |
| [heat-pump-panel.jpg](heat-pump-panel.jpg) | a heat pump's text LCD, running: FLOW 52.5°C | [heat-pump-boiler-panel.md](../heat-pump-boiler-panel.md) |
| [heat-pump-fault.jpg](heat-pump-fault.jpg) | the same panel with fault E04 LOW FLOW | [heat-pump-boiler-panel.md](../heat-pump-boiler-panel.md) |
| [boiler-seven-segment-temp.jpg](boiler-seven-segment-temp.jpg) | a boiler's two-digit display at 52 | [heat-pump-boiler-panel.md](../heat-pump-boiler-panel.md) |
| [boiler-seven-segment-fault.jpg](boiler-seven-segment-fault.jpg) | the same display showing E4 | [heat-pump-boiler-panel.md](../heat-pump-boiler-panel.md) |
| [washer-1-23.jpg](washer-1-23.jpg) | a washing machine with 1:23 left | [appliance-time-remaining.md](../appliance-time-remaining.md) |
| [washer-0-04.jpg](washer-0-04.jpg) | the same machine with 0:04 left | [appliance-time-remaining.md](../appliance-time-remaining.md) |
| [washer-0-00.jpg](washer-0-00.jpg) | finished, showing 0:00 | [appliance-time-remaining.md](../appliance-time-remaining.md) |
| [washer-end.jpg](washer-end.jpg) | finished, showing End | [appliance-time-remaining.md](../appliance-time-remaining.md) |
| [console-kernel-panic.png](console-kernel-panic.png) | a Linux console after a kernel panic | [server-console-ipmi.md](../server-console-ipmi.md) |
| [console-grub-rescue.png](console-grub-rescue.png) | GRUB's rescue prompt | [server-console-ipmi.md](../server-console-ipmi.md) |

`go test ./internal/docscheck` reads every frame with its recipe's watch and checks the result the recipe states. To try a recipe on its frame in the web UI, give a watch the source `ffmpeg:-i docs/recipes/samples/<frame>` (run watchglass from the repository folder; it needs ffmpeg) and press **Test this region**.

A frame of your own device makes a recipe better than any of these. If you have one, attach it to a [recipe request](https://github.com/darrenhuai/watchglass/issues/new?template=recipe.yml).

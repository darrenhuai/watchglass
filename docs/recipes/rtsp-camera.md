# RTSP camera

Most PoE security cameras and NVR channels don't expose a snapshot URL at
all — RTSP is the only thing they speak. RTSP is a video stream, not a
still image, so turning it into a frame needs a decoder. watchglass doesn't
carry one: it shells out to `ffmpeg`, grabs exactly one frame, and lets the
process exit.

## Watches config

```yaml
watches:
  - name: driveway-camera
    source: rtsp://admin:changeme@192.168.1.61:554/Streaming/Channels/101
    interval: 15s
    max_interval: 90s   # back off while nothing moves, snap back on change
    health_after: 3
    region: {x: 0, y: 0, w: 1, h: 1}
    trigger:
      type: pixel_change
      threshold: 15
      cooldown: 5m
    notify:
      - ntfy://ntfy.sh/example-driveway
```

## Tuning notes

**ffmpeg must be on PATH.** Prebuilt watchglass binaries don't bundle it —
`apt install ffmpeg`, `choco install ffmpeg`, or your distro's equivalent.
Docker images that already ship it (like the official watchglass image) need
nothing extra.

**Transport is always TCP.** watchglass invokes ffmpeg with
`-rtsp_transport tcp` on every RTSP grab — this isn't configurable, but it's
worth knowing: TCP retransmits, so a congested or Wi-Fi-bridged link won't
silently drop frames the way UDP transport can in a generic RTSP viewer.

**There is no persistent decoder.** Every poll spawns a fresh `ffmpeg`
process, connects, waits for a keyframe, grabs one frame, and exits — which
is the whole point (near-zero cost between polls) but also means an RTSP
watch costs noticeably more per poll than an HTTP snapshot watch. Don't set
`interval` unrealistically low; each grab is bounded at 20 seconds internally
before watchglass kills the process, so a camera that's slow to hand over a
first keyframe needs headroom above that in its poll interval, not below it.

Use `max_interval` to let a quiet camera settle into a slower poll rate —
useful here specifically because RTSP grabs aren't free. `health_after`
governs how many consecutive failed grabs (bad auth, camera reboot, network
blip) trigger a "stream unreachable" notification; the default of 3 is a
reasonable start for a wired camera on a stable network.

**If your camera can't do RTSP at all** — HomeKit Secure Video, Nest,
WebRTC-only doorbells — run [go2rtc](https://github.com/AlexxIT/go2rtc)
alongside watchglass and point at its snapshot endpoint instead:

```yaml
source: http://go2rtc-host:1984/api/frame.jpeg?src=cam1
```

That's a plain HTTP snapshot URL once go2rtc is doing the protocol
translation, so no ffmpeg is involved on the watchglass side — see
[generic-snapshot-camera.md](generic-snapshot-camera.md) for tuning that
shape.

## Caveats

- Credentials in the `source:` URL sit in `config.yaml` in plaintext — same
  posture as the MQTT and web-UI-auth passwords elsewhere in this file. The
  file is written `0o600` on Unix; keep it off shared filesystems.
- `rtsps://` (RTSP over TLS) is accepted too, but few consumer cameras
  actually speak it — check your camera's docs before assuming it does.
- A camera that goes down mid-stream (power loss, DHCP lease change) shows
  up as repeated grab failures, not a distinct error — `health_after`
  notifications are your only signal; there's no separate "camera offline"
  detection beyond failed grabs.

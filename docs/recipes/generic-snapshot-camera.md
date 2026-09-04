# Generic snapshot camera

Most IP cameras — PoE, Wi-Fi, baby monitors, cheap ONVIF boxes — serve a
plain JPEG at a fixed URL on request. That's the easiest source watchglass
supports: no ffmpeg, no persistent connection, just an HTTP GET once per
poll. Start here if you're not sure which recipe you need; every other
recipe in this gallery is a variation on the same `watches:` shape.

## Finding your snapshot URL

- Check the camera's admin page for a "snapshot" or "still image" link.
- Try common vendor paths: `/snapshot.jpg`, `/cgi-bin/snapshot.cgi`,
  `/Streaming/channels/1/picture` (Hikvision-style).
- If the camera is ONVIF-compliant, its `GetSnapshotUri` call (or any free
  ONVIF device-manager tool) will hand you the exact URL. watchglass doesn't
  speak ONVIF itself and has no camera-discovery feature — you find the URL
  once, then paste it into `source:`.
- If the endpoint needs HTTP Basic auth, put credentials right in the URL:
  `http://user:pass@192.168.1.42/snapshot.jpg` — watchglass's HTTP source is
  a stock Go `http.Client`, which sends the `Authorization` header for you
  when the URL carries a userinfo part.

## Watches config

```yaml
watches:
  # A status light (LED, indicator lamp) — no OCR needed, just watch for
  # the pixels in that spot to change.
  - name: workshop-status-light
    source: http://192.168.1.42/snapshot.jpg
    interval: 5s
    region: {x: 0.62, y: 0.10, w: 0.08, h: 0.08}
    trigger:
      type: pixel_change
      threshold: 20      # fire when >20% of the region's pixels change
      cooldown: 2m
    notify:
      - ntfy://ntfy.sh/example-workshop

  # The same camera also has a small text readout in frame — OCR it
  # instead, and fire whenever the text settles on something new.
  - name: workshop-readout
    source: http://192.168.1.42/snapshot.jpg
    interval: 10s
    region: {x: 0.30, y: 0.55, w: 0.40, h: 0.12}
    preprocess:
      grayscale: true
      threshold: 150
    trigger:
      type: ocr_changed
      confirm: 3
      cooldown: 1m
    notify:
      - ntfy://ntfy.sh/example-workshop
```

## Tuning notes

Open the watch in the web UI, drag a rectangle over the exact part of the
frame you care about, and hit **Test this region**. That single button is
the whole tuning workflow: it shows you the cropped, preprocessed image
next to what the OCR engine actually read, so you can adjust the region and
the `preprocess` sliders — grayscale, invert, Binarize (the `threshold`
field), upscale — against a live result instead of guessing. Keep the region as tight as you can — less
background means less noise for both `pixel_change` and OCR triggers.

`pixel_change` doesn't care what's in the frame; it's the fastest way to
get a working watch on day one, useful for lights, needles, anything where
"it changed" is enough. Swap to `ocr_match` (a specific pattern) or
`ocr_changed` (any new stable value) once you know what text you're after.

## Caveats

- The HTTP source decodes a single JPEG or PNG per request — it does not
  parse a multipart MJPEG stream. If your camera's "MJPEG URL" is actually
  a continuous multipart response rather than a single-image snapshot
  endpoint, look for a still-image path instead (most cameras that offer
  MJPEG streaming also offer a plain snapshot URL).
- Every poll is a fresh request; if the camera caches or rate-limits its
  snapshot endpoint, readings can lag behind what's really on screen.
- Basic auth via the URL only covers HTTP Basic — cameras that require a
  session cookie or a login form aren't supported.

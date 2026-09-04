# Server console / IPMI KVM

**If your BMC speaks Redfish or IPMI-over-LAN, query it directly** —
`ipmitool`, `freeipmi`, or a Redfish client will give you exact sensor
values with none of the caveats below. This recipe is for the other case:
an older BMC with no usable sensor API, a KVM-over-IP appliance sitting in
front of a machine you don't manage, or a management network you'd rather
not open a hole in — where video is the only thing you can get to, whether
that's a KVM's own snapshot/RTSP feed, or a capture card on a physical
console cable pointed at a monitor showing a sensor readout.

## Watches config

```yaml
watches:
  - name: db-server-cpu-temp
    # An IP-KVM's RTSP feed, or a USB capture card on the console cable
    # (v4l2:/dev/video0 on Linux, dshow:video=... on Windows).
    source: rtsp://192.168.1.70:554/kvm
    interval: 20s
    max_interval: 5m
    health_after: 6     # KVM/capture links blip more than a wired camera
    region: {x: 0.05, y: 0.90, w: 0.35, h: 0.08}
    preprocess:
      grayscale: true
      threshold: 160
    trigger:
      type: numeric
      pattern: "CPU Temp:\\s*([0-9]+)C"
      op: gt
      threshold: 85
      confirm: 2
      cooldown: 15m
    notify:
      - ntfy://ntfy.sh/example-db-server
```

## Tuning notes

`pattern` has a capture group — `([0-9]+)` — so watchglass parses just that
number rather than the first digits it finds anywhere in the region (which,
next to a label like "CPU Temp:", could otherwise latch onto a slot number
or a different reading entirely). Without a capture group the trigger falls
back to the first number in the OCR text; with one, only the matched group's
text is parsed.

`health_after: 6` instead of the default 3: KVM-over-IP sessions and USB
capture cards are more prone to a dropped frame or a brief HDMI handshake
hiccup than a normal IP camera, and a false "stream unreachable" alert on
hardware that's actually fine is worse than being a bit slower to notice a
real outage. Raise it further if your specific KVM is chattier than that.

`max_interval: 5m` backs off aggressively because server temperatures don't
swing fast — polling every 20 seconds is only useful right after a change,
not while things sit steady for hours.

**Keep `confirm` low for anything that reads continuously.** This is the
sharpest edge in the whole trigger model: a reading only counts toward
`confirm` when it's the *exact same OCR string* as the previous poll. A
temperature that legitimately moves by a degree every poll — or a display
whose OCR text is one-off different each time from minor image noise —
never accumulates enough identical consecutive reads to become "stable,"
and a numeric trigger with `confirm` set too high can end up never firing
at all. `confirm: 2` here is closer to a sanity check against a single
misread than a real debounce; watch the web UI's reading strip for a while
before trusting a value fluctuating faster than your `confirm` count can
keep up with.

## Caveats

- The `numeric` trigger fires once on crossing the threshold, not once per
  poll while above it — it needs to see the value drop back and cross again
  (past `cooldown`) to fire a second time.
- Nothing here validates that the OCR-extracted number is *the* CPU temp
  and not, say, a fan RPM that happens to be in the same region — get the
  region and pattern tight, and check the reading strip after any console
  layout change (a firmware update, a different KVM session resolution).
- "KVM console" video is often lower resolution and more compressed than a
  direct camera feed; small text that a camera would OCR fine can be
  unreadable over a lossy KVM link. If readings are consistently garbled,
  a higher KVM video-quality setting (where the appliance offers one) helps
  more than any `preprocess` tuning.

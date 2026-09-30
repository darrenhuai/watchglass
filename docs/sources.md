# Sources: where the frames come from

A watch's `source` is where watchglass gets a frame from each time it polls. For the URL your camera uses, see the [camera URL cookbook](recipes/camera-urls.md).

| Source | What it reads | Needs |
|---|---|---|
| `http://…` / `https://…` | a snapshot URL (one JPEG or PNG per request), or an MJPEG stream | nothing |
| `rtsp://…` / `rtsps://…` | an RTSP stream, over TCP | ffmpeg |
| `v4l2:/dev/video0` | a Linux webcam or capture card | ffmpeg |
| `dshow:video=Camera Name` | a Windows webcam or capture card | ffmpeg |
| `ffmpeg:<args>` | anything ffmpeg can open, including your own screen; the args go to ffmpeg as they are | ffmpeg |
| `demo:printer`, `demo:sevenseg` | the fake cameras built into watchglass | nothing |

A webcam or capture card can be opened by one program at a time, so grabs from the same device take turns: two watches on one webcam, or the detail page's snapshot while a poll is running, wait for each other instead of failing.

## Snapshot URLs and MJPEG streams

An `http(s)://` source is fetched once per poll. If the answer is a single image, that's the frame. If it's an MJPEG stream (`multipart/x-mixed-replace`, what an ESP32-CAM's `:81/stream`, OctoPrint's `?action=stream` or IP Webcam's `/video` send), watchglass reads the first frame and closes the connection, so the stream isn't held open between polls. Each fetch has 10 seconds to finish.

Images are recognised by their content, not their extension, so a `.jpg` URL that sends a PNG is fine.

### Logins

Put the user and password in the URL: `http://user:password@192.168.1.42/snapshot.jpg`.

- **Basic auth** works as it is.
- **Digest auth** works too (Hikvision, Dahua, Amcrest and many others). The very first request to a camera goes out with Basic, because watchglass can't know yet which the camera wants; after the camera answers with a Digest challenge, every request uses Digest and the password isn't sent again. MD5 and SHA-256 are supported.
- **A wrong password** costs one failed login per poll. Some cameras lock the account after a few, so fix it before leaving the watch running. The watch's page says "HOST turned down the login" when this happens.
- **Tokens and API keys** go in `headers:`, one `Name: value` per line, the way `curl -H` takes them:

  ```yaml
  headers:
    - "Authorization: Bearer <TOKEN>"
    - "X-Api-Key: <KEY>"
  ```

  `headers:` only exists in `config.yaml`, not in the web UI.

The watch's page never shows the password: the source line reads `http://user:xxxxx@…`. It is stored in plain text in `config.yaml`, so keep that file private ([security.md](security.md)).

### Self-signed certificates

Many cameras and KVMs (Reolink, PiKVM, UniFi) serve HTTPS with a certificate they signed themselves. watchglass refuses those by default, and the watch's page says so: "192.168.1.30's certificate isn't trusted". To accept it for one watch, tick **Certificate: Don't check it** under Polling (it shows for `https://` sources), or set:

```yaml
tls_insecure: true
```

The watch's header then shows a "certificate not checked" chip. The setting only affects that watch. `tls_insecure` and `headers` only apply to `http(s)://` sources.

## RTSP, webcams and other ffmpeg sources

For these, watchglass starts ffmpeg once per poll, grabs a single frame and lets it exit. There is no decoder running between polls, which is why a watch costs almost nothing while it waits: the whole program sits around 55 MB of RAM with a watch polling a 640x360 snapshot every 2 seconds, and needs no GPU. The flip side is that each RTSP poll has to connect and wait for a keyframe, so give RTSP watches an interval of 10 s or more. A grab that takes longer than 20 seconds is stopped.

- RTSP always runs over TCP (`-rtsp_transport tcp`), so a busy Wi-Fi link doesn't drop half the frame.
- `rtsps://` works where the camera offers it (UniFi Protect does).
- `ffmpeg:` splits its arguments on spaces, so a value can't contain one. `-f gdigrab -i desktop` works; `-i title=My Window` doesn't.
- ffmpeg isn't bundled with the binaries. The Docker image has it.

### Your own screen

ffmpeg can capture the screen of the machine watchglass runs on:

| System | Source |
|---|---|
| Windows | `ffmpeg:-f gdigrab -i desktop` |
| Linux (X11) | `ffmpeg:-f x11grab -i :0.0` |
| macOS | `ffmpeg:-f avfoundation -i 1` (the screen's device number; `ffmpeg -f avfoundation -list_devices true -i ""` lists them) |

To capture part of a big screen, add `-offset_x 0 -offset_y 0 -video_size 1280x720` before `-i` (gdigrab and x11grab). On Windows this only works while someone is logged in to that desktop, so not from a service. On macOS the terminal or app running watchglass needs Screen Recording permission. Wayland desktops don't allow x11grab.

### Cameras watchglass can't read directly

HomeKit, Nest, Ring and other WebRTC-only cameras: run [go2rtc](https://github.com/AlexxIT/go2rtc) next to watchglass and use its snapshot URL, `http://go2rtc-host:1984/api/frame.jpeg?src=cam1`. A camera that's already in Home Assistant can be read through HA's camera proxy (see the [cookbook](recipes/camera-urls.md)).

## When a stream dies

After `health_after` failed polls in a row (3 by default), whether the grab failed or the engine errored on the frame, a watch sends one "down" notification quoting the error, and one more when it comes back. It never repeats while the camera stays down, and it keeps polling the whole time. A frame that arrives and reads as blank (a dark display) is a normal poll with an empty reading, not a failure. The verdict survives a save: a watch that was down stays down until a poll produces a reading again, and recovers exactly once.

The alert titles are `watchglass: <name> (down)` and `watchglass: <name> (healthy)`, with bodies like `<name>: no reading for 3 consecutive polls: …` and `<name>: stream recovered`.

## Adaptive polling

Set `max_interval` above `interval` and a watch doubles its gap between polls while nothing changes, up to `max_interval`, and goes back to `interval` as soon as something does. It's off unless you set it. Backing off also stretches how long `confirm` takes in real time. A failed poll puts the watch straight back on its base interval, so noticing a dead stream is never delayed.

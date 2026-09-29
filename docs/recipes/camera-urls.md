# Camera URL cookbook

The `source` for a watch, by brand. Replace everything in `<ANGLE BRACKETS>`. If a camera has both, a snapshot URL is the better choice: one plain HTTP request per poll, no ffmpeg. MJPEG streams work as `http(s)` sources too: watchglass reads the first frame and hangs up.

**Status:** "run here" means the URL form was run through watchglass on the maintainer's machine. "From vendor docs" means it comes from the linked documentation and hasn't been run against the real device; if it doesn't work for yours, please [open an issue](https://github.com/darrenhuai/watchglass/issues/new?template=recipe.yml) with what does.

## IP cameras

| Brand | Source | Login | Gotchas | Status |
|---|---|---|---|---|
| Reolink | `http://<CAMERA>/cgi-bin/api.cgi?cmd=Snap&channel=0&rs=watchglass&user=<USER>&password=<PASSWORD>` | in the URL's query | Add `&width=640&height=360` for a smaller frame. Newer models serve HTTPS with a self-signed certificate: use `https://` and `tls_insecure: true`. | from [vendor docs](https://support.reolink.com/articles/360007011233-How-to-Capture-Live-JPEG-Image-of-Reolink-Cameras-via-Web-Browsers/) |
| Hikvision | `http://<USER>:<PASSWORD>@<CAMERA>/ISAPI/Streaming/channels/101/picture` | Digest, in the URL | Channel 101 is the main stream, 102 the substream. Digest fails if the camera's clock is more than a few minutes off; turn on NTP. | from [vendor docs](https://www.hikvisioneurope.com/eu/portal/portal/Technical%20Materials/24%20How%20To/CCTV/How%20to%20use%20API%20to%20capture%20picture.pdf) |
| Dahua, Amcrest | `http://<USER>:<PASSWORD>@<CAMERA>/cgi-bin/snapshot.cgi?channel=1` | Digest, in the URL | Recent firmware accepts only Digest, which watchglass speaks. | from [vendor docs](https://s3.amazonaws.com/amcrest-files/Amcrest+HTTP+API+3.2017.pdf) |
| TP-Link Tapo | `rtsp://<USER>:<PASSWORD>@<CAMERA>:554/stream2` | the camera account you create in the Tapo app, not your TP-Link login | No snapshot URL, so it needs ffmpeg. `stream2` is the smaller stream and plenty for reading a display; `stream1` is full resolution. | from [vendor docs](https://www.tp-link.com/us/support/faq/2680/) |
| Wyze | `rtsp://<BRIDGE>:8554/<CAMERA_NAME>` | as set in the bridge | Wyze cameras need [docker-wyze-bridge](https://github.com/mrlt8/docker-wyze-bridge) running, which needs a Wyze API key. | from [vendor docs](https://github.com/mrlt8/docker-wyze-bridge) |
| UniFi Protect | `rtsps://<NVR>:7441/<STREAM_ID>` | in the stream ID | Turn on the RTSP stream in the camera's settings in Protect and copy the RTSPS URL it shows. | from [vendor docs](https://www.home-assistant.io/integrations/unifiprotect/) |
| Any ONVIF camera | the URL the camera returns for `GetSnapshotUri` | usually Digest | Any ONVIF tool (ONVIF Device Manager, for example) shows the snapshot URL. watchglass doesn't speak ONVIF itself. | from the [ONVIF Media Service spec](https://www.onvif.org/specs/srv/media/ONVIF-Media-Service-Spec.pdf) (GetSnapshotUri) |

## Boards, phones and printers

| Device | Source | Login | Gotchas | Status |
|---|---|---|---|---|
| ESP32-CAM (the CameraWebServer example) | `http://<BOARD>/capture` | none | `/capture` is a snapshot. The MJPEG stream is on another port, `http://<BOARD>:81/stream`, and allows one viewer at a time, so a browser watching it can lock watchglass out. Prefer `/capture`. | from [vendor docs](https://github.com/espressif/arduino-esp32/blob/master/libraries/ESP32/examples/Camera/CameraWebServer/app_httpd.cpp) |
| ESPHome camera | `http://<BOARD>:8081/` | none | Needs `esp32_camera_web_server` with a `mode: snapshot` entry on that port; a `mode: stream` port works too. | from [vendor docs](https://esphome.io/components/esp32_camera_web_server/) |
| Android phone with [IP Webcam](https://play.google.com/store/apps/details?id=com.pas.webcam) | `http://<PHONE>:8080/shot.jpg` | as set in the app | `/video` is the MJPEG stream. Keep the phone on a charger and stop it sleeping. | from the app's own start page: open `http://<PHONE>:8080/` in a browser and it lists every URL it serves |
| OctoPrint / OctoPi webcam | `http://<OCTOPI>/webcam/?action=snapshot` | none | `?action=stream` is MJPEG. If OctoPrint already reports what you want, use its API instead. | from [vendor docs](https://docs.octoprint.org/en/master/configuration/config_yaml.html) |

## Through other software

| Software | Source | Login | Gotchas | Status |
|---|---|---|---|---|
| Frigate | `http://<FRIGATE>:5000/api/<CAMERA>/latest.jpg` | none on port 5000 | Port 5000 is Frigate's unauthenticated internal port; it's often only reachable from the same Docker network. The authenticated port 8971 needs a login watchglass can't do. | from [vendor docs](https://docs.frigate.video/integrations/api/latest-frame-camera-name-latest-extension-get/) |
| go2rtc | `http://<GO2RTC>:1984/api/frame.jpeg?src=<STREAM>` | none by default | The way in for cameras watchglass can't read directly: HomeKit, Nest, WebRTC-only doorbells. | from [vendor docs](https://github.com/AlexxIT/go2rtc) |
| Home Assistant camera | `http://<HOME_ASSISTANT>:8123/api/camera_proxy/camera.<NAME>` | `headers: ["Authorization: Bearer <LONG_LIVED_TOKEN>"]` | Any camera HA already shows. Create the token under your HA profile, Security tab. | from [vendor docs](https://developers.home-assistant.io/docs/api/rest/) |
| PiKVM | `https://<PIKVM>/api/streamer/snapshot` | `headers: ["X-KVMD-User: <USER>", "X-KVMD-Passwd: <PASSWORD>"]` | Self-signed certificate: needs `tls_insecure: true`. See the [KVM recipe](server-console-ipmi.md). | from [vendor docs](https://docs.pikvm.org/api/) |
| NanoKVM | `http://<NANOKVM>/api/stream/mjpeg` | a login cookie, see the KVM recipe | | from [source](https://github.com/sipeed/NanoKVM/blob/main/server/router/stream.go) |

## Your own screen

| System | Source | Status |
|---|---|---|
| Windows | `ffmpeg:-f gdigrab -i desktop` | run here (Windows 11, ffmpeg 9); options in the [ffmpeg docs](https://ffmpeg.org/ffmpeg-devices.html#gdigrab) |
| Windows, part of the screen | `ffmpeg:-f gdigrab -offset_x 0 -offset_y 0 -video_size 1280x720 -i desktop` | run here; options in the [ffmpeg docs](https://ffmpeg.org/ffmpeg-devices.html#gdigrab) |
| Linux (X11) | `ffmpeg:-f x11grab -i :0.0` | from [ffmpeg docs](https://ffmpeg.org/ffmpeg-devices.html#x11grab) |
| macOS | `ffmpeg:-f avfoundation -i 1` | from [ffmpeg docs](https://ffmpeg.org/ffmpeg-devices.html#avfoundation) |

All of these need ffmpeg and the desktop of a logged-in user. On a multi-monitor Windows machine, `desktop` is every screen side by side; draw the box on the one you want. macOS needs Screen Recording permission for the app that starts watchglass, and `1` is the screen's device number (`ffmpeg -f avfoundation -list_devices true -i ""` lists them). Wayland desktops don't allow x11grab. `ffmpeg:` splits its arguments on spaces, so window titles with spaces in them can't be used; capture the part of the screen the window sits on instead.

## A snapshot URL that isn't in this list

- Look in the camera's web page for "snapshot" or "still image", or right-click its live picture and copy the image address.
- Try the common paths: `/snapshot.jpg`, `/cgi-bin/snapshot.cgi`, `/image.jpg`, `/jpg/image.jpg`.
- Test a URL in a browser first. If it shows a picture, it works in watchglass.

More on logins, certificates and streams: [sources.md](../sources.md).

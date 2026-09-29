# watchglass — Home Assistant Add-on

## Experimental

The main repository doubles as an add-on repository: `repository.yaml` at its root, this directory as the add-on. The add-on runs the published `ghcr.io/darrenhuai/watchglass` image on amd64 and aarch64, the two architectures the Supervisor allows. It hasn't been submitted to the official store and hasn't had wide testing on real Supervisors, so expect rough edges and please [report them](https://github.com/darrenhuai/watchglass/issues/new?template=bug.yml). The plain Docker route in the [install guide](https://github.com/darrenhuai/watchglass/blob/master/docs/install.md) is the better-trodden path.

## Installing

Settings → Add-ons → Add-on store → ⋮ → Repositories, paste `https://github.com/darrenhuai/watchglass`, then install **watchglass**. Or use the one-click link:

[Add the watchglass repository to Home Assistant](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fdarrenhuai%2Fwatchglass)

## Before you start it: add a password

The add-on has no ingress. It publishes port 8080 on every network interface of your Home Assistant machine, so anyone on your network can open the dashboard, change watches and make watchglass fetch URLs for them. It logs a `WARNING` about this at every start until you add a password.

On first start watchglass creates an empty `config.yaml` in the add-on's config folder (`/addon_configs/<id>_watchglass/` on the host, `/config` inside the container). Open it with the File editor or Samba add-on, add an `auth:` block, and restart the add-on:

```yaml
auth:
  username: admin
  password: <PASSWORD>

watches: []
```

## Using it

Open `http://<home-assistant-host>:8080` and log in. Add a watch with your camera's snapshot or RTSP URL (the [camera URL cookbook](https://github.com/darrenhuai/watchglass/blob/master/docs/recipes/camera-urls.md) lists them by brand; a camera that is already in Home Assistant can be read through HA's camera proxy), drag a box over the part of the screen you care about, press **Test this region**, pick a trigger and save. The image includes tesseract and ffmpeg, so text, seven-segment displays and RTSP all work. `engine: rapidocr` doesn't: the image has no Python.

No camera yet? The empty watch list has an **Add a demo watch** button that creates a watch on a built-in fake printer screen.

## Home Assistant entities via MQTT

To get each watch into Home Assistant as a device, add an `mqtt:` block pointing at the Mosquitto broker add-on, with an HA user you created for watchglass:

```yaml
mqtt:
  broker: tcp://core-mosquitto:1883
  username: watchglass
  password: <PASSWORD>
```

Restart the add-on after changing the block. The top of the dashboard then says **Home Assistant: connected**, or why it isn't.

Each watch becomes a device named `watchglass <watch name>` with a **Reading** sensor (the text read, once it has held for a few polls), **Health** (online or offline), **Motion** (on for 30 seconds when the trigger fires) and **Snapshot** (the crop from the last fire). A `numeric` watch also gets a **Value** sensor HA can graph; give the watch a unit and device class under **Home Assistant** on its page to get long-term statistics. The details are in [docs/home-assistant.md](https://github.com/darrenhuai/watchglass/blob/master/docs/home-assistant.md).

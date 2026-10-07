# watchglass Home Assistant add-on

## Experimental

The main repository doubles as an add-on repository: `repository.yaml` at its root, this directory as the add-on. The add-on runs the published `ghcr.io/darrenhuai/watchglass` image on amd64 and aarch64, the two architectures the Supervisor allows. It hasn't been submitted to the official store and hasn't had wide testing on real Supervisors, so expect rough edges and please [report them](https://github.com/darrenhuai/watchglass/issues/new?template=bug.yml). The plain Docker route in the [install guide](https://github.com/darrenhuai/watchglass/blob/master/docs/install.md) is the better-trodden path.

The sidebar integration (ingress) was tested against a simulated Supervisor proxy that strips the prefix and sets the headers the way Home Assistant's source does, not against a live Home Assistant. If the sidebar page comes up unstyled or a link lands on a Home Assistant error page, that is the untested part: the add-on log will say `ingress: ignoring X-Ingress-Path` if the header wasn't what the docs describe. Please report it with that line.

## Installing

Settings → Add-ons → Add-on store → ⋮ → Repositories, paste `https://github.com/darrenhuai/watchglass`, then install **watchglass**. Or use the one-click link:

[Add the watchglass repository to Home Assistant](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fdarrenhuai%2Fwatchglass)

## Opening it

Start the add-on and open **watchglass** in the Home Assistant sidebar (or **Open web UI** on the add-on's page). It runs behind Home Assistant's own login: you are already signed in, so watchglass asks for nothing more, and no port is open on your network. The sidebar entry is for Home Assistant admin users, which is Home Assistant's rule for add-on panels.

On first start watchglass creates an empty `config.yaml` in the add-on's config folder (`/addon_configs/<id>_watchglass/` on the host, `/config` inside the container). Everything the dashboard saves goes there; the File editor or Samba add-on can edit it too, and the add-on needs a restart after edits made by hand.

## Direct access, if you want it

The dashboard can also be reached without the sidebar, for a bookmark on a phone or a script that posts to the Test route. Map port 8080 under the add-on's **Network** settings, and it answers at `http://<home-assistant-host>:8080`.

Before you do that, add an `auth:` block to `config.yaml` and restart the add-on. A mapped port is open to everyone on your network, with no login of its own: anyone who reaches it can change watches and make watchglass fetch URLs from inside your network. Without the block, the add-on log says so at every start. Other add-ons on the Supervisor's internal network can reach the add-on's port directly even when it isn't mapped; add the block if you don't trust every add-on you run.

```yaml
auth:
  username: admin
  password: <PASSWORD>

watches: []
```

The login applies to direct requests only. The sidebar keeps working as before: Home Assistant has already checked who you are, and watchglass trusts requests that come from the Supervisor.

## Using it

Add a watch with your camera's snapshot or RTSP URL (the [camera URL cookbook](https://github.com/darrenhuai/watchglass/blob/master/docs/recipes/camera-urls.md) lists them by brand; a camera that is already in Home Assistant can be read through HA's camera proxy), drag a box over the part of the screen you care about, press **Test this region**, pick a trigger and save. The image includes tesseract and ffmpeg, so text, seven-segment displays and RTSP all work. `engine: rapidocr` doesn't: the image has no Python.

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

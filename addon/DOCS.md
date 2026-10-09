# watchglass Home Assistant add-on

## Experimental

The main repository doubles as an add-on repository: `repository.yaml` at its root, this directory as the add-on. It runs on amd64 and aarch64, the two architectures the Supervisor allows. It hasn't been submitted to the official store, so expect rough edges and please [report them](https://github.com/darrenhuai/watchglass/issues/new?template=bug.yml). The plain Docker route in the [install guide](https://github.com/darrenhuai/watchglass/blob/master/docs/install.md) is the better-trodden path.

It was last checked on Home Assistant 2026.10.0 with Supervisor 2026.09.3 on amd64: installing, the sidebar page with every link and form staying under Home Assistant's address, the Mosquitto add-on, a mapped port with a login, restarts, rebuilds and updates. That ran in Home Assistant's own add-on development container, not on Home Assistant OS and not on a Raspberry Pi. If the sidebar page comes up unstyled or a link lands on a Home Assistant error page, look for `ingress: ignoring X-Ingress-Path` in the add-on log and please report it with that line.

## Installing

Settings → Apps → Install app → ⋮ → Repositories, press **Add**, paste `https://github.com/darrenhuai/watchglass` and add it, then install **watchglass**. On Home Assistant versions from before add-ons were renamed apps it's Settings → Add-ons → Add-on Store → ⋮ → Repositories, where you paste the address straight away. Or use the one-click link:

[Add the watchglass repository to Home Assistant](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fdarrenhuai%2Fwatchglass)

The install builds a small image on your machine: the published `ghcr.io/darrenhuai/watchglass` image plus a start-up script (this directory's `Dockerfile` and `run.sh`). The script starts as root only to hand the add-on's config folder to the unprivileged user watchglass runs as, then runs watchglass as that user. Updates rebuild it the same way.

Every version of the add-on up to 0.1.10 stopped at once with `open /config/config.yaml: permission denied` in the log. Update the add-on and start it again; your `config.yaml` is kept.

## Opening it

Start the add-on, turn on **Show in sidebar** on its Info tab (Home Assistant leaves it off for a new add-on), and open **watchglass** in the sidebar. **Open Web UI** on the Info tab opens the same page. It runs behind Home Assistant's own login: you are already signed in, so watchglass asks for nothing more, and no port is open on your network. The sidebar entry is for Home Assistant admin users, which is Home Assistant's rule for add-on panels.

On first start watchglass creates an empty `config.yaml` in the add-on's config folder: `/app_configs/97c7415f_watchglass/` on the host (`/addon_configs/` on older versions), `/config` inside the container. Everything the dashboard saves goes there, next to the history database, and both survive restarts, rebuilds and updates. The dashboard keeps your comments when it saves. The File editor or Samba add-on can edit `config.yaml` too; restart the add-on after edits made by hand, which also gives the files back to watchglass's user if another add-on wrote them.

## Direct access, if you want it

The dashboard can also be reached without the sidebar, for a bookmark on a phone or a script that posts to the Test route. On the add-on's Configuration tab, under **Network**, turn on **Show disabled ports**, type a host port next to `8080/tcp` and save. The dashboard then answers at `http://<home-assistant-host>:<port>`.

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

With one seven-segment watch the add-on used about 17 MB of memory. With a tesseract text watch added it used about 30 MB, briefly up to 50 MB while tesseract reads. Either way it stayed well under 1% of a CPU. The Info tab shows the live figures.

## Home Assistant entities via MQTT

Install and start the official **Mosquitto broker** add-on. Home Assistant then finds the broker and offers the MQTT integration under Settings → Devices & services; add it. Create a Home Assistant user for watchglass (Settings → People → Add person, with **Allow login** on) and put its login in an `mqtt:` block in watchglass's `config.yaml`:

```yaml
mqtt:
  broker: tcp://core-mosquitto:1883
  username: watchglass
  password: <PASSWORD>
```

Restart the add-on after changing the block. The top of the dashboard then says **Home Assistant: connected**, or why it isn't.

Each watch becomes a device named `watchglass <watch name>` with a **Reading** sensor (the text read, once it has held for a few polls), **Health** (online or offline), **Motion** (on for 30 seconds when the trigger fires) and **Snapshot** (the crop from the last fire). A `numeric` watch also gets a **Value** sensor HA can graph; give the watch a unit and device class under **Home Assistant** on its page to get long-term statistics. The details are in [docs/home-assistant.md](https://github.com/darrenhuai/watchglass/blob/master/docs/home-assistant.md).

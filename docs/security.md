# Security

watchglass listens on `127.0.0.1:8080`, so out of the box only the machine it runs on can open the web UI. Anyone who can reach the UI can change watches and add sources, and watchglass fetches every source URL itself, so an open UI also lets people make requests from inside your network. Put a password on it before you let anything else reach it.

## Authentication

Add an `auth:` block to `config.yaml` to put the web UI behind HTTP Basic auth:

```yaml
auth:
  username: admin
  password: <PASSWORD>
```

Restart watchglass after adding it. Leave the block out for no auth, which is fine while you stay on localhost. The password is stored in plain text, like the MQTT and camera passwords in the same file. On Linux and macOS watchglass writes `config.yaml` readable only by its owner (mode 0600); keep it off shared folders and out of backups you publish.

What the UI shows and hides:

- A camera password is never shown on a watch's page: the source line reads `http://user:xxxxx@…`.
- Notification URLs, tokens included, are shown in the watch's Notify box, because that's where they're edited. Everywhere else (delivery errors, the watch list, the log, test results) they're cut down to the service and host.

## Reaching it from other machines

`-listen 0.0.0.0:8080` listens on every interface. If you do that without an `auth:` block, watchglass logs a `WARNING` at every start.

In Docker, watchglass always listens on `0.0.0.0:8080` inside the container, and the port mapping decides who can reach it. The compose file maps `127.0.0.1:8080:8080`, which keeps it on the host; the start-up log says so in one line instead of a warning. Add `auth:` before you change the mapping to `8080:8080`.

The Home Assistant add-on publishes port 8080 on every interface of the HA host, so it does log the warning. Add an `auth:` block to its `config.yaml` first thing.

## Behind a reverse proxy

A reverse proxy in front of watchglass can add HTTPS and its own login. On its own subdomain or port, nothing special is needed.

To serve it under a path, like `https://home.example.com/watchglass/`, use `-base-path` with a proxy that **strips the prefix** before forwarding:

```nginx
location /watchglass/ {
    proxy_pass http://127.0.0.1:8080/;
}
```

The trailing slash on both lines is what makes nginx strip `/watchglass/`. Then start watchglass with:

```bash
watchglass -base-path /watchglass
```

watchglass still serves at `/`; `-base-path` only puts the prefix in front of every link, form and redirect it writes, so the browser's next request carries the prefix the proxy is about to strip. It must start with `/`, and a trailing slash is ignored. Because watchglass never writes an absolute URL to itself, it doesn't read `X-Forwarded-Host` or `X-Forwarded-Proto`, and the proxy needs no extra headers. `-base-path` changes URLs only; the `auth:` block still applies.

## Reporting a security problem

Please don't put the details of a vulnerability in a public issue. Open an issue that only says you have a security problem to report, and the maintainer will set up a private way to send it.

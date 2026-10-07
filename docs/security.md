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

The Home Assistant add-on opens through ingress (below) and publishes no port unless you map one in its Network settings. If you do, add an `auth:` block to its `config.yaml` first: the mapped port is open to your whole network, and the start-up log says so.

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

## Home Assistant ingress

The add-on opens inside Home Assistant: the Supervisor proxies `/api/hassio_ingress/<token>/…` on Home Assistant's own address to the add-on, strips that prefix, and names it in the `X-Ingress-Path` header. Home Assistant checks the login before anything is forwarded. watchglass supports this in ingress mode (`-ingress`, or `WATCHGLASS_INGRESS=1`, which the add-on sets):

- A request is an ingress request only when it comes from the Supervisor's address (`172.30.32.2`, the `-ingress-from` default) and carries an `X-Ingress-Path` of exactly the documented shape: `/api/hassio_ingress/` followed by one token of letters, digits, `-` and `_`. Anything else in the header (a second slash, `..`, quotes, a scheme, line breaks, or the header sent more than once) is ignored as a whole, and the add-on log says so once.
- An ingress request gets that prefix on every link, form, redirect and cookie path, and skips the `auth:` login: Home Assistant already did it, and a second prompt inside the sidebar would be wrong.
- Every other request is a direct request and is answered exactly as with ingress off: links bare (or with `-base-path`), the `auth:` block enforced. So with the port mapped, someone on your network can reach the dashboard directly, with the password if you set one and without one if you didn't, but can't borrow the sidebar's login by sending the header: it only counts from the Supervisor's address. Other add-ons on the Supervisor's internal network can reach the add-on's port directly too, mapped or not; add an `auth:` block if you don't trust every add-on you run.
- With ingress mode off (the default everywhere but the add-on), the header does nothing, so a reverse proxy or a client can't steer links on an ordinary install.
- Cross-site request protection keeps working through the proxy: the page and its forms share Home Assistant's origin, and the Supervisor passes the browser's `Origin`, `Host` and `Sec-Fetch-Site` headers through unchanged.

This was tested against a simulated Supervisor proxy built from Home Assistant's source, not a live one; the add-on is experimental. Ingress is the Supervisor's own protocol, so there's no reason to turn it on anywhere else.

## Reporting a security problem

Please don't put the details of a vulnerability in a public issue. Open an issue that only says you have a security problem to report, and the maintainer will set up a private way to send it.

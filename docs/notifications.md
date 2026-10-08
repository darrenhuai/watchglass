# Notifications

Each watch has a list of notification URLs, one per line, in the Notify box on its page (`notify:` in `config.yaml`). When the watch fires, or its camera goes down or comes back, every URL gets the alert.

The URLs are [shoutrrr](https://shoutrrr.nickfedor.com/latest/services/overview/) URLs (the maintained nicholas-fedor fork), plus a built-in ntfy sender that attaches a picture. Replace everything in `<ANGLE BRACKETS>`:

| Service | URL |
|---|---|
| ntfy.sh | `ntfy://ntfy.sh/<TOPIC>` |
| Your own ntfy server | `ntfy+http://192.168.1.10:8081/<TOPIC>` (or `ntfy://` for HTTPS) |
| Any webhook, as a JSON POST | `generic+http://192.168.1.10:9000/<PATH>?template=json` (or `generic://` for HTTPS) |
| Discord | `discord://<TOKEN>@<WEBHOOK_ID>` |
| Telegram | `telegram://<BOT_TOKEN>@telegram?chats=<CHAT_ID>` |
| Slack (incoming webhook) | `slack://hook:<TOKEN>@webhook`, where the token is the three parts of the webhook address joined with `-` |
| Pushover | `pushover://shoutrrr:<APP_TOKEN>@<USER_KEY>/` |
| Email | `smtp://<USER>:<PASSWORD>@<SMTP_HOST>:587/?fromaddress=<FROM>&toaddresses=<TO>` |
| Gotify | `gotify://<GOTIFY_HOST>/<APP_TOKEN>` |
| Matrix | `matrix://<USER>:<PASSWORD>@<HOST>:<PORT>/?rooms=<ROOM>` |

The [shoutrrr docs](https://shoutrrr.nickfedor.com/latest/services/overview/) list every service and its options.

## ntfy, with the picture

An ntfy topic on ntfy.sh is public to anyone who knows its name, so pick one nobody would guess (`printer-7f3kq2`, not `printer`). Install the [ntfy app](https://ntfy.sh), subscribe to the topic, and put `ntfy://ntfy.sh/<TOPIC>` in Notify.

Alerts sent to ntfy carry the cropped image of the region that fired, so the push on your phone shows the actual screen. Other services get the text only.

## Check it before you rely on it

- **Send test notification**, under the Notify box, sends a test to every URL in the box as it is typed, before you save. Each line gets its own result: "Test sent", or why it didn't arrive (the server's answer, a refused connection, a wrong token). Nothing is saved.
- **Saving refuses a URL that can't work** and says why. For the common mistakes it offers the right form with a **Use this** button:
  - a web address for an ntfy topic, like `https://ntfy.sh/<TOPIC>`, becomes `ntfy://ntfy.sh/<TOPIC>`
  - a Discord webhook link becomes `discord://<TOKEN>@<WEBHOOK_ID>`
  - a Slack webhook link becomes `slack://hook:<TOKEN>@webhook`
  - any other `http(s)://` address becomes `generic+http(s)://…?template=json`
  - an email address explains the `smtp://` form, and a malformed Telegram token says what a BotFather token looks like
- **Delivery status.** After an alert, the watch's Live panel says "Last alert sent at …" or why it couldn't be delivered, and the watch list marks the watch "alerts failing" until a later alert gets through. The error names the line and the service, never the token. After a restart, until the watch alerts again, the line says when it last fired and whether that alert went out, "before watchglass restarted".

## What an alert looks like

| Event | Title | Body |
|---|---|---|
| The trigger fired | `watchglass: printer` | `printer: pattern matched — PRINT COMPLETE` |
| The camera stopped answering | `watchglass: printer (down)` | `printer: no reading for 3 consecutive polls: …` |
| It came back | `watchglass: printer (healthy)` | `printer: stream recovered` |

The watch's name is in the body as well as the title, because a plain webhook only gets the body. A `generic` URL with `?template=json` receives `{"title": "…", "message": "…"}`.

## When the service is on the same machine

If watchglass runs in Docker, `127.0.0.1` in a notify URL is the container, not your machine. Use `host.docker.internal` (the compose file maps it) or the machine's LAN address. The test result says this when a loopback address refuses the connection.

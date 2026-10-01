# Showing the wall display with DisplayOps

[DisplayOps](https://simpledisplayops.com) turns a Raspberry Pi into a managed screen: you flash an image, pair it, and tell it which URL to show. This guide puts the collector's wall display (`/dashboard`) on a Pi screen that way.

You do not need this service to use the wall display. Any browser in full-screen or kiosk mode pointed at `/dashboard` works.

> The portal steps below follow the DisplayOps documentation at the time of writing, which is marked beta. If a screen looks different, their docs at <https://simpledisplayops.com/docs> are the authority.

- [What you need](#what-you-need)
- [1. Flash and pair the Pi](#1-flash-and-pair-the-pi)
- [2. Choose the URL](#2-choose-the-url)
- [3. Assign it to the screen](#3-assign-it-to-the-screen)
- [4. Check it](#4-check-it)
- [Screen sizes](#screen-sizes)
- [If the web interface has a password](#if-the-web-interface-has-a-password)
- [Useful extras](#useful-extras)
- [Troubleshooting](#troubleshooting)

## What you need

- A DisplayOps account.
- A Raspberry Pi the service supports. At the time of writing that is a Pi 4 (2 GB or more), Pi 5 or Pi 400/500; a Pi 3B+ works for lighter content on a best-effort basis.
- A screen attached to the Pi. The wall display is designed for a 7-inch 800×480 panel and scales up to 1080p.
- The collector running, **version 1.1.0 or newer**, and reachable from the Pi. The Pi and the collector must be on the same network, or routed to each other.

Test the last point from any machine on the Pi's network: opening `http://<collector>:8080/dashboard` in a browser should show the display.

## 1. Flash and pair the Pi

1. In the portal, go to **Displays → Pair a display** and download the DisplayOps image. Verify the checksum shown next to the download.
2. Write it to a microSD card with Raspberry Pi Imager (**Use custom**, then pick the `.img.xz`). You can preload Wi-Fi in the imager's advanced options; Ethernet needs nothing.
3. Put the card in the Pi and connect the screen and power. After about 40 seconds the screen shows a six-character pairing code and a QR code.
4. In the portal choose **Pair a display**, enter the code and name the screen.

## 2. Choose the URL

| URL | What the screen shows |
|---|---|
| `http://<collector>:8080/dashboard` | Everything at once: current download and upload, the live chart, top devices, top destinations, today's totals, and the status strip |
| `http://<collector>:8080/dashboard?rotate=true` | The same numbers and chart with larger type; the lower panel cycles through top devices, top destinations and today's totals |
| `http://<collector>:8080/dashboard?rotate=true&interval=20` | As above, 20 seconds per panel instead of 12 |

Pick by viewing distance. Everything-at-once suits a screen on a desk; rotating suits one across a room.

Use the collector's **IP address or a name the Pi can resolve**, and the port you set for the web interface.

## 3. Assign it to the screen

Give the screen a **Website** content item with the URL from step 2.

Leave the **refresh interval** empty. The page updates itself over a WebSocket, reconnects by itself after a network interruption, and reloads when the collector is upgraded. A periodic refresh would only make it blink.

The screen switches to the display within a few seconds.

From the API, the same thing is:

```sh
curl -X POST https://app.simpledisplayops.com/api/v1/displays/DISPLAY_ID/content \
  -H "Authorization: Bearer $SDO_KEY" -H "Content-Type: application/json" \
  -d '{"type":"website","url":"http://<collector>:8080/dashboard"}'
```

## 4. Check it

The strip along the bottom of the display tells you its state:

| Strip | Meaning |
|---|---|
| Green dot, **Live**, with connection and device counts | Everything works |
| **Delayed: flow records only** | The collector has no router API configured; numbers are about a minute behind |
| Amber: **Router API unreachable** | The collector lost its API session to the router; rates are delayed until it returns |
| Amber or red banner with a title | An alert is firing. The Alerts page in the main interface has the details |
| Red: **No flow records from the router** | The router has stopped exporting, or cannot reach the collector |
| Red: **Collector unreachable, reconnecting** | The Pi cannot reach the collector. The page keeps trying by itself |

You can also take a live screenshot of the screen from the DisplayOps portal.

## Screen sizes

The display sizes itself from the screen it is on. Plain `/dashboard` is correct for every screen; there is nothing to configure per resolution.

| Screen | Layout |
|---|---|
| 800×480, 1024×600 (7-inch panels) | Four rows per list |
| 1024×768 and other 4:3 screens | More rows per list, taller chart |
| 1280×720, 1920×1080 | Same layout as 800×480, scaled up, with a ten-minute chart |

To preview a small screen on a desktop browser, add `?resolution=800x480` (or `1024x768`, `1080p`, any `WIDTHxHEIGHT`). The page is then laid out for exactly that size and scaled to fit the window. It can be combined with the other options: `/dashboard?resolution=800x480&rotate=true`.

If the picture is rotated or does not fill the panel, that is a display setting on the Pi, not something the page controls.

## If the web interface has a password

When `NFP_AUTH_PASSWORD` is set on the collector, `/dashboard` asks for it with HTTP basic authentication instead of the login form, because an unattended screen cannot fill in a form.

On the content item, tick **The page asks for a username and password**, then enter any user name and the collector's web password. DisplayOps stores the password encrypted and answers the browser's prompt itself.

Do not put the password in the URL.

## Useful extras

- **Screen-on hours.** Use a schedule on the display to switch the screen off at night.
- **Rotation with other pages.** A slideshow can alternate the wall display with other URLs. Give it at least 30 seconds per turn so the chart has time to be read.
- **Restart from your desk.** The portal's refresh, restart-player and reboot commands work as for any other content; the page needs none of them in normal operation.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Blank page or "cannot be reached" | The Pi cannot reach the collector. Check that the address and port are right and that the Pi's network (for example a guest Wi-Fi) is allowed to reach the collector's |
| Red "Collector unreachable" banner | Same cause, but it worked before: the collector is down or restarting. The page recovers by itself |
| "Today" shows dashes | The collector is older than 1.1.0. Upgrade it |
| The two big figures stay at 0.0 while the lists and clock move | The collector is older than 1.2.2. Kiosk players that render off-screen never deliver animation frames, which earlier versions used to move the figures. Upgrade it |
| The page is the full web interface, not the wall display | The URL is missing `/dashboard` |
| Text is too small to read from where you sit | Add `?rotate=true` |
| A password prompt appears on the screen | The collector has a web password. See [above](#if-the-web-interface-has-a-password) |
| Names are MAC-like (`Apple 5f:9c`) instead of host names | The collector has no router API configured, so it cannot read DHCP names. Rename devices on the Devices page of the main interface |
| The screen shows an old layout after an upgrade | It reloads itself on the first update after the collector restarts. If it does not, send a refresh from the portal |

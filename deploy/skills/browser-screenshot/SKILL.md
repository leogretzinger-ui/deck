---
name: browser-screenshot
description: Take a screenshot of a web page, such as an app preview on a 127.0.0.x address, a local dev server or any public URL, and save it to the project's Assets panel. Use when you need to look at a page, show someone how a page looks, or check a visual change. Needs the Deck browser overlay.
---

# Browser screenshot

Deck's browser overlay runs Chromium as a Playwright service and sets
`NOISE_BROWSER_WS` in every session. This skill drives that browser with the
Playwright client the Deck image ships, so it works from any project.

## Take a screenshot

Run this from the project root, the directory the session started in:

```sh
node /opt/noisedeck/skills/browser-screenshot/screenshot.mjs <url> [name] [--width <pixels>]
```

- `<url>` is any http or https address. A bare loopback host such as
  `127.0.0.2:8840` or `localhost:5173` gets `http://`; any other bare host
  gets `https://`. The browser shares this container's network, so app
  previews and dev servers on 127.0.0.x are reachable as they are.
- `name` is the file name, without a folder. `.png` is added if you leave it
  off. Without a name the file is named from the host and the current time.
  A file with the same name is replaced.
- `--width` sets the viewport width in pixels (200 to 4000, default 1280).
  The viewport is 800 pixels tall.

The script waits for the page's load event and saves a PNG of the viewport
to `.deck/assets/` in the current directory.

## What to tell the person you are working with

- On success the script prints the saved path. Tell them the screenshot is in
  the Assets panel and give its file name. If it says the page answered with
  an HTTP error, say so: the image shows an error page.
- If it says the browser overlay is not enabled, tell them that this Deck has
  no browser overlay, so you cannot take screenshots, and stop. Do not install
  a browser or a different Playwright version.
- If it cannot reach the browser or load the page, report the message it
  printed. Do not claim you saw the page.

# visionary-browser

Real user monitoring for [Visionary](https://github.com/styropyr0/visionary):
page loads, Web Vitals, single-page app route changes, API calls and errors,
measured in your users' browsers and shown on Visionary's **Frontend** page.

- Zero runtime dependencies, about 4.3 KB gzipped (`<script>` build).
- Web Vitals (LCP, INP, CLS, FCP, TTFB) measured with `PerformanceObserver`,
  rated with Google's thresholds.
- Batches events and flushes with `sendBeacon` when the page is hidden or
  closed, so nothing is lost on navigation.

The package is published as `visionary-browser`.

## Install

With a bundler:

```sh
npm install visionary-browser
```

```js
import Visionary from "visionary-browser";

Visionary.init({
  endpoint: "https://visionary.example.com", // your Visionary server
  key: "pk_live_…",                           // a public client key
  app_version: "2.4.1",
  version: import.meta.env.VITE_COMMIT_SHA,   // optional build or commit
});
```

Or with a script tag, as early in `<head>` as possible:

```html
<script src="https://unpkg.com/visionary-browser@0.1.0/dist/visionary.min.js"></script>
<script>
  Visionary.init({ endpoint: "https://visionary.example.com", key: "pk_live_…", app_version: "2.4.1" });
</script>
```

## Server setup

On the Visionary server:

- `VISIONARY_CLIENT_KEYS`: comma-separated public client keys (or create one in
  Settings). Client keys can only write events; they are safe to ship in a page.
- `VISIONARY_CLIENT_ORIGINS`: the origins allowed to send, e.g.
  `https://app.example.com,https://*.example.com`. Empty allows any origin.
- How long events are kept: Settings → Data → Retention, Frontend events (default 30 days).
- `VISIONARY_CLIENT_RATE_PER_IP`, `VISIONARY_CLIENT_RATE_PER_KEY`: requests per
  minute (defaults 120 and 6000; 0 turns a limit off).

## API

| Call | What it does |
|---|---|
| `init(options)` | Starts collecting. Call once. |
| `setUser(id \| null)` | Attaches a user id to the events from now on. |
| `track(name, value?, attrs?)` | Records a custom event, e.g. `track("checkout_step", 2, { step: "address" })`. |
| `captureException(error, attrs?)` | Records a handled error. |
| `flush()` | Sends what is queued now. |

Options:

| Option | Default | |
|---|---|---|
| `endpoint` | required | The Visionary server's URL. |
| `key` | required | A public client key. |
| `app_version` | | Your app's version name. |
| `version` | | Build or commit. |
| `user_id` | | The signed-in user, if known at startup. |
| `sampleRate` | `1` | Share of sessions (browser tabs) that report, 0 to 1. A tab is either fully in or fully out. |
| `ignoreUrls` | `[]` | fetch/XHR URLs to skip, as substrings or regular expressions. |
| `app` | `"web"` | |

## What is collected

| Event | When | Data |
|---|---|---|
| `page_load` | after the load event | path (ids as `:id`), load time, TTFB, FCP, DOM interactive |
| `page_load` with `soft: 1` | each `pushState`, `replaceState` or back/forward that changes the path | time until the DOM has been quiet for 100 ms |
| `web_vital` | LCP at the first input or when hidden; CLS and INP when the page is hidden; FCP and TTFB at load | value and rating |
| `api` | every `fetch` and `XMLHttpRequest` except calls to Visionary itself | `METHOD /path` with ids as `:id`, status (0 on network failure), duration as the user waited |
| `error` | uncaught errors, unhandled promise rejections, `captureException` | type, message, stack |

URLs never include query strings or fragments. A path segment becomes `:id`
when it is all digits, a UUID, 16 or more hex characters, or 20 or more
characters containing a digit. Each session is a browser tab (a random id in
`sessionStorage`); there are no cookies.

## Build

```sh
npm install
npm run build   # dist/index.js (ESM), dist/visionary.min.js (IIFE), types
```

The build fails if the script build grows over 6 KB gzipped.

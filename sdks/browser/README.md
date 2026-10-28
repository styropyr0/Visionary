# visionary-browser

Real user monitoring for [Visionary](../../Readme.md). Add one `init()` call
and the dashboard's **Frontend** page shows what your users see: page loads,
Web Vitals (LCP, INP, CLS, FCP, TTFB), single-page app route changes, the API
calls the page makes and the errors it hits.

Unlike the server SDKs, it does not touch a database. It sends events to
your Visionary server's ingest endpoint with a public client key. It has no
runtime dependencies, is about 4.3 KB gzipped (`<script>` build), and uses no
cookies.

## Quick start

About 5 minutes. You need a running Visionary server (see
[Get started](../../Readme.md#get-started)) that your users' browsers can
reach.

**1. Create a client key.** In the dashboard, open **Settings → API keys**,
choose **Create key** with the **Client ingest** scope, and copy the key (it
is shown once). Client keys can only send events, so they are safe to ship in
a page. (You can also enter your own keys under **Settings → Server →
Ingest → Client keys**.)

**2. Allow your site's origin.** In **Settings → Server → Ingest**, add your
site to **Allowed web origins**, e.g. `https://app.example.com` or
`https://*.example.com`. While the list is empty, any origin may send.
This setting applies after the Visionary server restarts.

**3. Install and initialise.** With a bundler:

```sh
npm install visionary-browser
```

```js
import Visionary from "visionary-browser";

Visionary.init({
  endpoint: "https://visionary.example.com", // your Visionary server
  key: "pk_live_…",                           // the client key from step 1
  app_version: "2.4.1",                       // optional
});
```

Or with a script tag, as early in `<head>` as possible:

```html
<script src="https://unpkg.com/visionary-browser@0.1.0/dist/visionary.min.js"></script>
<script>
  Visionary.init({ endpoint: "https://visionary.example.com", key: "pk_live_…", app_version: "2.4.1" });
</script>
```

Call `init()` once, as early as possible, so the page load and early errors
are seen.

**4. Check it works.** Open your site, click around, then switch tabs or
close it (that flushes the last batch). Within a few seconds the visit shows
on **Frontend**. In the browser's network tab you should see `POST
.../api/ingest/client` answered with `204`.

## Recipes

### Single-page apps (React, Vue, Svelte, Angular)

Nothing extra. Route changes made with `history.pushState`, `replaceState`
or back/forward are recorded as page loads with `soft: 1`, timed until the
DOM has been quiet for 100 ms. Call `init()` once in the app's entry file
(`main.tsx`, `main.ts`), not inside a component.

Hash routers (`/#/settings`) change only the fragment, which is not tracked,
so their route changes do not show as page loads. Use the router's history
mode to get them.

### Server-rendered apps (Next.js, Nuxt, SvelteKit)

`init()` does nothing when there is no `window`, so importing the package on
the server is safe. Call it from code that runs in the browser, e.g. a
client component's effect:

```jsx
"use client";
import { useEffect } from "react";
import Visionary from "visionary-browser";

export function Monitoring() {
  useEffect(() => {
    Visionary.init({ endpoint: process.env.NEXT_PUBLIC_VISIONARY_URL, key: process.env.NEXT_PUBLIC_VISIONARY_KEY });
  }, []);
  return null;
}
```

A second `init()` call is ignored.

### The signed-in user

```js
Visionary.setUser(user.id);   // after sign-in
Visionary.setUser(null);      // after sign-out
```

Events from then on carry the id; the queue is flushed first so earlier
events keep the previous user. If the user is known at startup, pass
`user_id` to `init()` instead.

### Errors

Uncaught errors and unhandled promise rejections are recorded on their own.
Errors your code or framework catches never reach the page, so report them:

```js
try { ... } catch (err) {
  Visionary.captureException(err, { feature: "checkout" });
}
```

React error boundaries and Vue's error handler catch render errors, so add
the call there:

```jsx
// React
class Boundary extends React.Component {
  componentDidCatch(error) { Visionary.captureException(error); }
  // ...
}
```

```js
// Vue
app.config.errorHandler = (err) => Visionary.captureException(err);
```

Errors are grouped by fingerprint on **Frontend**.

### Custom events

```js
Visionary.track("checkout_step", 2, { step: "address" }); // name, value?, attrs?
```

### API calls

Every `fetch` and `XMLHttpRequest` the page makes is recorded as `METHOD
/path` with its status (0 on a network failure) and the time the user waited,
except calls to Visionary itself. To skip some:

```js
Visionary.init({ ..., ignoreUrls: ["/health", /\/analytics\//] });
```

Compared with the server's own time for the same route, the difference is
network and client time. When the response carries `X-Request-ID` (every
Visionary server SDK sets it), the call is linked to the server's request and
its logs: open a session under Frontend → Recent sessions to see both sides
on one timeline. For calls to another origin, the API must list
`X-Request-ID` in `Access-Control-Expose-Headers`.

### Logs

Send the console's lines to Logs, with the session, user and page (they
still print as before; at most 120 lines a minute are sent):

```js
Visionary.init({ ..., captureConsole: true, logLevel: "warn" }); // logLevel defaults to "info"
Visionary.log("error", "checkout button did nothing", { step: "pay" });
```

### Releases

Pass the build or commit so errors and vitals can be compared across
deploys:

```js
Visionary.init({ ..., app_version: "2.4.1", version: import.meta.env.VITE_COMMIT_SHA });
```

### Sampling

```js
Visionary.init({ ..., sampleRate: 0.1 }); // 10% of browser tabs report
```

A tab is either fully in or fully out, so sampled sessions stay whole.

### Content Security Policy

With a CSP, allow the Visionary server in `connect-src`, and the script's
host in `script-src` if you load it from unpkg:

```text
Content-Security-Policy: script-src 'self' https://unpkg.com; connect-src 'self' https://visionary.example.com
```

Installing from npm and bundling avoids the `script-src` entry.

## What is collected

| Event | When | Data |
|---|---|---|
| `page_load` | after the load event | path (ids as `:id`), load time, TTFB, FCP, DOM interactive |
| `page_load` with `soft: 1` | each `pushState`, `replaceState` or back/forward that changes the path | time until the DOM has been quiet for 100 ms (at most 10 s) |
| `web_vital` | LCP at the first input or when hidden; CLS and INP when the page is hidden; FCP and TTFB at load | value and rating (Google's thresholds) |
| `api` | every `fetch` and `XMLHttpRequest` except calls to Visionary | `METHOD /path` with ids as `:id`, status, duration |
| `error` | uncaught errors, unhandled rejections, `captureException` | type, message, stack |
| `custom` | `track()` | name, value, attributes |

Every batch also carries the app name, `app_version`, `version`, a session
id, the user id and the device and OS.

URLs never include query strings or fragments. A path segment becomes `:id`
when it is all digits, a UUID, 16 or more hex characters, or 20 or more
characters containing a digit. A session is one browser tab (a random id in
`sessionStorage`).

Delivery: events are batched (sent at 30 events or after 5 s) with `fetch`
and the key in the `Authorization` header. When the page is hidden or closed,
the queue goes out with `sendBeacon`, which cannot set headers, so the key
travels in the query string. A `5xx` or network failure is retried once after
15 s; a `4xx` answer drops the batch. At most 1000 events are queued while
offline.

## Options

| Option | Default | What it does |
|---|---|---|
| `endpoint` | required | The Visionary server's URL. `/api/ingest/client` is added unless it is already there. |
| `key` | required | A client ingest key. |
| `app_version` | | Your app's version name, e.g. `2.4.1`. |
| `version` | | Build or commit. |
| `user_id` | | The signed-in user, if known at startup. |
| `sampleRate` | `1` | Share of sessions (browser tabs) that report, 0 to 1. |
| `ignoreUrls` | `[]` | fetch/XHR URLs to skip, as substrings or regular expressions. |
| `app` | `"web"` | App name on every event. |
| `captureConsole` | `false` | Sends console lines to Logs. |
| `logLevel` | `"info"` | The lowest console level sent: `debug`, `info`, `warn` or `error`. |

## API

| Call | What it does |
|---|---|
| `init(options)` | Starts collecting. Call once; later calls are ignored. |
| `setUser(id \| null)` | Attaches a user id to the events from now on. |
| `track(name, value?, attrs?)` | Records a custom event. |
| `captureException(error, attrs?)` | Records a handled error. |
| `log(level, message, attrs?)` | Sends a line to Logs with this session. |
| `flush()` | Sends what is queued now. |

## Server settings

All set in the dashboard, not in environment variables:

- Client keys: **Settings → API keys** (scope **Client ingest**), or your
  own keys in **Settings → Server → Ingest → Client keys**.
- **Allowed web origins** and the rate limits (**Frontend events per IP per
  minute**, default 120; **per key per minute**, default 6000; 0 turns a
  limit off): **Settings → Server → Ingest**. These apply after the server
  restarts.
- How long events are kept: **Settings → Data**, Frontend events.

## Troubleshooting

Open the browser's developer tools, Network tab, and filter on
`ingest/client`. The status of the `POST` usually says what is wrong.

| Symptom | Cause | Fix |
|---|---|---|
| No `ingest/client` requests at all | `init()` is not called, or is called without `endpoint` or `key` (it then does nothing, silently). Or the tab was not sampled (`sampleRate` below 1). | Check the call and its options. Wait 5 s or hide the tab to force a flush. |
| `401 invalid client key` | Wrong key, a revoked or expired key, or a server ingest or read key. | Create a key with the **Client ingest** scope in **Settings → API keys**. |
| `403 origin not allowed` | Your site's origin is not in **Allowed web origins**, or it was added but the server has not restarted since. | Add it exactly (scheme, host and port if any, no path), or `https://*.example.com` for subdomains, then restart the server. |
| CORS error in the console, no status | The endpoint is wrong (it points at your app, not the Visionary server), or a proxy in front of the server strips the CORS headers. | Use the Visionary server's public URL. Let `OPTIONS` and `POST` to `/api/ingest/client` through untouched. |
| `blocked by Content Security Policy` | `connect-src` does not include the Visionary server. | Add it; see [Content Security Policy](#content-security-policy). |
| `net::ERR_BLOCKED_BY_CLIENT` | An ad or tracking blocker blocks the request or the unpkg script. | Bundle the package instead of loading it from unpkg, and serve the Visionary server from your own domain (e.g. `visionary.example.com`). Some users will still block it. |
| `429 too many requests` | The per-IP or per-key rate limit was hit. | Raise it in **Settings → Server → Ingest** and restart the server, or lower `sampleRate`. |
| `503 ingest queue full` | The server cannot write fast enough. | Check the server's database; lower `sampleRate`. |
| Route changes do not show | The app uses a hash router. | Use history mode. |
| Caught errors do not show | The framework catches them before the page does. | Call `captureException` in the error boundary or error handler. |

## Build

```sh
npm install
npm run build   # dist/index.js (ESM), dist/visionary.min.js (IIFE), types
```

The build fails if the script build grows over 6 KB gzipped. The event format
is defined in [`sdks/SPEC.md`](../SPEC.md), "Client ingest API".

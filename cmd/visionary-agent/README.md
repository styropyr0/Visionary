# visionary-agent: the host agent

**This is not Visionary itself.** `visionary-agent` is a small helper you
install on a machine you want to watch. It sends that machine's numbers to a
Visionary server you already run. It has no dashboard, stores nothing, and
does nothing without a server to send to.

| You want | Use |
|---|---|
| The Visionary server and dashboard | the Docker image `ghcr.io/styropyr0/visionary` ([Run the server](../../Readme.md#2-run-the-server)) |
| To watch a whole machine: its drives, processes, services, Docker containers and SSH logins | this agent, installed on that machine |
| To watch your app (requests, errors, its own CPU and memory) | an SDK in the app ([SDKs](../../Readme.md)) |

There is no Docker image of the agent. It runs on the machine itself as a
systemd service, because it reads the machine's processes, logs and container
engine directly. An older `ghcr.io/styropyr0/visionary-agent` image from
before v0.5.0 is no longer built or updated.

## What it reports

Every 15 s it samples CPU, memory, swap, disks, network and load; once a
minute it sends them with:

- the machine's details, every drive, the top processes and failed systemd
  services (shown in **System**);
- every Docker or Podman container: state, health, restarts, CPU and memory
  against its limits, network, disk, and OOM kills, crashes and failed health
  checks (shown in **Containers**);
- SSH logins, sudo, newly opened ports, pending updates and reboots (shown in
  **Security**, Linux only), and:
  - SSH keys added to or removed from any `authorized_keys` file
    (`ssh_key_added`, `ssh_key_removed`; the type, `SHA256:` fingerprint and
    comment, never the key);
  - changes to accounts, sudoers, cron, PAM, `sshd_config`,
    `ld.so.preload`, `rc.local` and systemd units in `/etc/systemd/system`
    (`file_changed`: created, modified, deleted or permissions changed, and
    any new UID 0 account in `/etc/passwd`; never the contents);
  - processes run from `/tmp`, `/var/tmp` or `/dev/shm` or from a deleted
    file, known miners, shells wired to a network socket and netcat running a
    program (`suspicious_process`);
  - running containers that are privileged, share the host's network or PIDs,
    mount the Docker socket or add `SYS_ADMIN` (`container_privileged`), run
    as root (`container_root`), or run an image the machine had not run
    before (`container_new_image`).

  Keys, files and new images are compared with what the agent saw at start,
  so starting it reports none of them; it keeps nothing on disk. Each finding
  about a process or container is sent once. Without root the agent sees
  only what its user can read.

## Install

On the machine to watch, with your server's URL and a **server** ingest key
(Settings → API keys):

```sh
curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \
  | sudo VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=<server key> sh
```

The installer downloads `visionary-agent-linux-amd64` or `-arm64` from the
latest GitHub Release. Its settings are in
[the main README](../../Readme.md#host-agent).

Each security check can be turned off on its own (all are on by default, and
none runs when `VISIONARY_AGENT_SECURITY` is off):

| Setting | Flag | Turns off |
|---|---|---|
| `VISIONARY_AGENT_SSH_KEYS=off` | `-ssh-keys=false` | `authorized_keys` changes |
| `VISIONARY_AGENT_FILE_INTEGRITY=off` | `-file-integrity=false` | system file changes |
| `VISIONARY_AGENT_PROCESS_CHECKS=off` | `-process-checks=false` | suspicious processes |
| `VISIONARY_AGENT_CONTAINER_RISKS=off` | `-container-risks=false` | privileged, root and new-image containers |

## Logs

The agent sends the machine's logs to Logs when you name the sources. It
sends none by default:

```sh
VISIONARY_AGENT_LOGS=journald,docker,/var/log/nginx/*.log
```

| Source | What it sends |
|---|---|
| `journald` | the systemd journal, with each entry's unit, priority as the level, and identifier as the service |
| `docker` | the output of every running Docker or Podman container (stdout and stderr), named by its compose service or name |
| a file glob | lines appended to matching files, following rotation (rename or truncate); the service is the file's directory, or its name under `/var/log` |

Lines are sent in gzipped batches every 2 seconds to `/api/ingest/logs` with
the server key; the server reads JSON, logfmt and access-log lines, scrubs
secrets and applies Settings → Logs. Where each source got to is kept in
`VISIONARY_AGENT_STATE` (default `/var/lib/visionary-agent`), so a restart
neither repeats nor loses lines; a source seen for the first time starts at
its end.

## Uptime locations

Started with `VISIONARY_AGENT_LOCATION`, the agent stops reporting the host and
becomes an uptime location: it runs the checks Visionary assigns to that
location, from this machine, with the same code the server runs its own
checks with. Use it to see a check from another region, or from inside a
private network.

1. In Visionary, open **Uptime → Locations**, add a location (say
   `frankfurt-vps`) and copy its key. It is shown once; **New key** replaces it.
2. Run the agent with that key and name:

   ```sh
   VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=<location key> \
   VISIONARY_AGENT_LOCATION=frankfurt-vps visionary-agent
   ```

   Or in Docker, from this repository: `deploy/location/compose.yaml`, or
   `docker build --target location` (`--target location-browser` adds
   Chromium).
3. Pick the location in a check's form, next to or instead of **server**.

Every 15 s the agent polls `/api/agent/v1/location/poll` for the location's
checks, runs them on their schedule, and posts results to
`/api/agent/v1/location/results` in batches every 5 s. HTTP checks always
run; browser checks run when a Chrome or Chromium is found on the machine
(`VISIONARY_CHROME` points at one), which the poll reports, and the form
greys out browser checks for locations without one. Only one runner per
location runs checks: a second one started with the same key stands by
until the first stops. Checks may not reach private or loopback addresses
unless `VISIONARY_AGENT_ALLOW_PRIVATE=true` (or the server allows it for its
own checks).

| Variable | Default | |
|---|---|---|
| `VISIONARY_AGENT_LOCATION` | none | The location's name; turns location mode on. |
| `VISIONARY_AGENT_ALLOW_PRIVATE` | `false` | Let checks reach private, loopback and link-local addresses. |
| `VISIONARY_AGENT_BROWSER_WORKERS` | `1` | Browser checks run at once. |
| `VISIONARY_CHROME` | auto | The browser for browser checks. |

## Build

```sh
go build ./cmd/visionary-agent
```

# visionary-agent: the host agent

**This is not Visionary itself.** `visionary-agent` is a small helper you
install on a machine you want to watch. It sends that machine's numbers to a
Visionary server you already run. It has no dashboard, stores nothing, and
does nothing without a server to send to.

| You want | Use |
|---|---|
| The Visionary server and dashboard | the Docker image `ghcr.io/styropyr0/visionary` ([Run the server](../../Readme.md#run-the-server)) |
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
  **Security**, Linux only).

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

## Build

```sh
go build ./cmd/visionary-agent
```

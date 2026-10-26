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

## Build

```sh
go build ./cmd/visionary-agent
```

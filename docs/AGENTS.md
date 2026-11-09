# Agents

> Single-file, end-to-end reference for AI coding agents setting up or using grid. Reading this once gives the full model: hub, node, keys, configs, job files, CLI, Docker, events, failures, deploy, and the test kit.

- **Module:** `github.com/diphyx/grid/v2` (Go 1.27); one binary, `grid`
- **Images:** `ghcr.io/diphyx/grid-hub`, `ghcr.io/diphyx/grid-node`; tags `2` (the generation), `2.1`, `2.1.0`, `latest`
- **Install:** `brew install diphyx/tap/grid`, or a binary from the GitHub releases (macOS, Linux, Windows; amd64, arm64); `grid version` prints it
- **Protocol:** `grid/2`, port `9420`, `Noise_IK_25519_ChaChaPoly_SHA256`, CBOR; exact in `spec/SPEC.md`.
- **Versions:** the major is the generation (`docs/versions.md`). Minor and patch releases are drop-in. A new major is a fresh install: its configs say `grid = <major>`, its state is in `/var/lib/grid/<major>/`, its protocol is `grid/<major>`; nothing of another major is read or talked to.

---

## 1. Mental Model

grid sends jobs to machines and brings the results back. What a node can run is fixed by the node: its config defines named **cmds**. A **job** is a TOML file that pushes files in, runs some of those cmds as steps (it can only add `args` and `env`, never new commands), and pulls files back. The **hub** takes the job, picks a **node** that offers the job's **tag** and has room under its `[jobs] limit`, and runs it there in a **space** (the job's own folder).

```
job file (TOML) → hub → node that offers the job's tag
                         └── space: made at the start, deleted at the end
                             ├── push  → files in
                             ├── steps → the node's cmds, one at a time
                             └── pull  → files back to the hub
```

| Term  | Meaning                                                                                   |
| ----- | ----------------------------------------------------------------------------------------- |
| hub   | the coordinator: knows the nodes, holds the jobs, places, follows, retries, keeps results |
| node  | a worker that runs jobs; it never decides anything                                        |
| tag   | a label for a kind of work; a node offers tags, a job asks for one                        |
| cmd   | a command the node defines under a name; a job can run only these, adding `args`/`env`    |
| step  | one cmd run inside a job, with its own `args` and `env`                                   |
| space | the job's folder on the node                                                              |
| share | a push a node keeps for later jobs, read only, linked into their spaces                   |

## 2. Keys

```bash
# hub.key (0600) + hub.pub
grid key new /etc/grid/hub

# node.key (0600) + node.pub
grid key new /etc/grid/node

# print the public key again
grid key pub /etc/grid/hub.key
```

- Raw X25519, 32 bytes, base64 (44 chars); not SSH keys. Make them with `grid key new`.
- Each side holds the **other** side's public key, always, with or without `signal`: the hub needs it to dial the node; the node accepts only its hubs' keys. A `.key` never leaves its machine.
- In configs, `identity` is your own private key, a path only; `trust` is the other side's public key, itself or a path to its file.
- `grid key new` refuses to overwrite.

## 3. Node Config — `/etc/grid/node.toml`

```toml
grid = 2                         # required: the generation, grid version's major
identity = "/etc/grid/node.key"  # required
tags = ["build"]                 # required
signal = true                    # send beat + state changes to the hub; default false
port = 9420                      # default

[[hubs]]                         # prod; at least one: who may call this node; each IP once
ip = "10.0.0.1"
trust = "/etc/grid/prod.pub"
port = 9420                      # where the hub listens, with signal

[[hubs]]                         # staging: its own jobs, the same pool and share
ip = "10.0.0.3"
trust = "/etc/grid/staging.pub"

[jobs]
path = "/var/lib/grid/2/node/jobs" # default
limit = 4                        # jobs at once; default 1
expires = "10m"                  # idle job cleanup; "0" = never (default)

[history]                        # the event log: job, share, node, hub
path = "/var/lib/grid/2/node/history"
limit = "100MB"                  # default; "0" = off
expires = "30d"                  # "0" = never (default)

[share]                          # share pushes, kept for the jobs after
path = "/var/lib/grid/2/node/share"
limit = "10GB"                   # "0" = off (default)
expires = "7d"                   # a share no job used this long goes; "0" = never (default)

[cmds.build]
exec = ["make", "-j8"]
timeout = "1h"                   # "0" = never
```

- A job is the hub's that opened it: `unknown` to every other hub.
- `[jobs] limit` and the share are one for all hubs.
- With `signal`: beat to every hub, a job's posts only to its own.

## 4. Hub Config — `/etc/grid/hub.toml`

```toml
grid = 2                         # required: the generation, grid version's major
identity = "/etc/grid/hub.key"   # required
signal = true                    # listen for the nodes' beat and post; default false
port = 9420                      # where it listens, with signal

[jobs]
path = "/var/lib/grid/2/hub/jobs" # default; also the packed inputs, under tars/
expires = "30d"                  # finished jobs and files; default "30d"; "0" = never

[history]                        # the event log: job, node, hub
path = "/var/lib/grid/2/hub/history"
limit = "100MB"                  # default; "0" = off

[[nodes]]                        # at least one
tags = ["build"]
ip = "10.0.0.2"
trust = "/etc/grid/node.pub"
port = 9420
share = true                     # a hint: its share is on; default false
```

**TOML rules that bite:**

- Top-level keys must come **before** the first `[table]`; a key after `[[hubs]]` belongs to that hub.
- Durations are **strings**: `"10m"`, `"30d"`, `"0"`. A bare number (`expires = 600`) is rejected.
- Sizes are **strings** with a unit, binary: `"512MB"`, `"10GB"` (2³⁰), `"0"`. A bare number is rejected.
- Unknown keys are errors, named with their line: `unknown keys: retry (line 2)`.
- A config without `grid = 2`, or for another generation, is refused before any other check: `config is for grid 1, this is grid 2`.

## 5. Job File — any name, TOML

```toml
tag = "build"
retries = 2                      # if the node is lost, run it again up to 2 more times

[[push]]                         # repeatable; before the steps
from = "./src"                   # local, relative to the job file
to = "src"                       # in the space; default the space itself
share = false                    # true: the node keeps it for later jobs, read only, linked at to

[[steps]]                        # repeatable; run in order
cmd = "build"                    # a cmd the node defines
args = ["linux"]                 # appended to its exec
env = { CC = "clang" }           # GRID_* is reserved

[[pull]]                         # repeatable; after the steps
path = "out"                     # a file or folder in the space
```

States: `queued` → `running` → `done` | `failed` | `stopped`.

- `queued`: no node with the tag has room; retried every 2 s.
- A failed step skips the rest; files are **still pulled**; the job is `failed`. So is a step past its `timeout`; only kill makes a job `stopped`.
- Pushes are packed at submit; the local files are not needed later. The hub keeps each packed input once, known by its files' names, sizes, and times, and does not pack it again unchanged; `--repack` forces it.
- `share = true`: the node keeps the push in its share and links it at `to` (required, not the space); the next job with the same content gets it with no transfer. A cmd must not write there. No other push or pull may be at `to` or under it, and no pull may hold it. A node with share off takes it as a normal push. The tail shows `push <to>: hit | installed | normal`.

## 6. What a Cmd Sees

| Thing      | Value                                                                                              |
| ---------- | -------------------------------------------------------------------------------------------------- |
| cwd        | the space                                                                                          |
| argv       | the cmd's `exec` + the step's `args`                                                               |
| env        | node env + step `env` + `GRID_JOB` (the node's job id, not the hub's), `GRID_SPACE`, `GRID_EVENTS` |
| own events | append lines `[name] key=value …` to `$GRID_EVENTS`; they arrive as type `evt.<name>`              |
| exit `0`   | step `done`; else `failed`, message = the last stderr line, or the error                           |
| kill       | SIGTERM to the process group, SIGKILL after 10 s → `stopped`                                       |
| exit       | the step ends; whatever is left in its process group is killed                                     |

Event lines: `[name]` required (`[a-z0-9._-]{1,64}`), then optional `key=value` pairs (`"quoted"` values allowed), up to 8 KiB; values arrive as text; any other line becomes an `err` output line.

## 7. CLI

```bash
# node
grid node run [--config /etc/grid/node.toml]
grid node status | jobs | job <id> | tail <id> [--after N] | history [--kind K] [--hub IP]

# hub; submit prints the id, and --wait exits 0 only if the job is done
grid hub run [--config /etc/grid/hub.toml]
grid hub submit <job.toml> [--wait] [--repack]
grid hub status | nodes | jobs | job <id> | tail <id> [--after N] | history [--kind K]
grid hub pull <id> <dir>
grid hub kill <id> | drop <id>

# test kit
grid test node | hub | vectors

# this binary's version and protocol: grid v2.0.0 (protocol grid/2)
grid version
```

Flags: `--json`, `--watch`. Every command but `run` talks to the daemon on the same machine.

## 8. Docker

```dockerfile
FROM ghcr.io/diphyx/grid-node
COPY cmds/ /opt/cmds/
```

- Config and keys in `/etc/grid`; state in `/var/lib/grid` (`2/node/` and `2/hub/` under it); runs as uid `100` (`grid`): `chown 100` mounted private keys.
- Each container has its own IP, so hub and node both use `9420`; in the configs, use the container IPs.
- Health checks: `grid node status`, `grid hub status`.

## 9. Events

`{seq, time, type, data}`; `seq` from 1, no gaps.

| Type         | `data`                                |
| ------------ | ------------------------------------- |
| `log`        | `stream`, `line`                      |
| `state`      | step `cmd`, `state`, `message`        |
| `job`        | job `state`, `message`                |
| `note`       | hub `text`, `node`, `step`            |
| `evt.<name>` | a cmd's own event: its pairs, as text |

## 10. Failures

| Case         | Result                                                         |
| ------------ | -------------------------------------------------------------- |
| node lost    | rerun from the start elsewhere, up to `retries`; then `failed` |
| lost means   | 15 s without `beat` (signal), or calls failing for 30 s        |
| hub restart  | running jobs resume on their node                              |
| node restart | jobs stay; a running cmd → `failed`, "node restarted"          |
| kill         | immediate, also mid-transfer → `stopped`                       |

## 11. Deploy

| Setup                            | IPs in the configs     | Open TCP `9420`                                                                       |
| -------------------------------- | ---------------------- | ------------------------------------------------------------------------------------- |
| one machine, containers          | the container IPs      | nothing; they share a Docker network                                                  |
| local network, several machines  | private IPs (LAN, VPC) | on each node to the hub; on the hub to the nodes with `signal`                        |
| public network, several machines | public IPs             | the same, limited to the other side's IP; a node behind NAT needs a reachable address |

## 12. Test Kit

| Command             | Checks                                                                                                                                                                                |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `grid test node`    | a node: 106 checks; needs the kit cmds (`pkg/kit/cmds/node.toml`), `tags = ["kit"]`, `[jobs] limit = 2`; `--share` with a `[share] limit`; `--other-*` with a second `[[hubs]]` entry |
| `grid test hub`     | a hub: every frame it sends; that it drops its jobs                                                                                                                                   |
| `grid test vectors` | the golden bytes in `spec/vectors.json`                                                                                                                                               |

Exit code = failed checks, up to 125. The kit talks to your running program over TCP, so the language does not matter: check your encoder against `spec/vectors.json` first, then point the kit at it. Passing the kit makes any implementation a grid hub or node.

## 13. Common Mistakes

- Swapping private keys instead of `.pub` files.
- `signal = true` on the node but not the hub: the node keeps dialing and nobody answers.
- A cmd that is not in the node's `[cmds.*]`: the job fails at that step, "no cmd".
- Keys after a `[table]` in TOML.
- Editing a config without restarting: both sides read it only at start (jobs survive the restart).
- A job whose tag no node offers: it stays `queued` forever.
- Forgetting `9420` in the firewall: the node must accept the hub's IP; the hub must accept the nodes' IPs only with `signal`.

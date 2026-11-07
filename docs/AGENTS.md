# Agents

> Single-file, end-to-end reference for AI coding agents setting up or using grid. Reading this once gives the full model: hub, node, keys, configs, job files, CLI, Docker, events, failures, setups, and the test kit.

- **Module:** `github.com/diphyx/grid` (Go 1.27); one binary, `grid`
- **Images:** `ghcr.io/diphyx/grid-hub`, `ghcr.io/diphyx/grid-node`; tags `latest`, `1.2.3`, `1.2`
- **Install:** `go install github.com/diphyx/grid@latest` (or `@v1.2.3`), or a binary from the GitHub releases (macOS, Linux, Windows; amd64, arm64); `grid version` prints it
- **Protocol:** `grid/1`, port `9420`, `Noise_IK_25519_ChaChaPoly_SHA256`, CBOR; exact in `spec/SPEC.md`. Releases are versioned on their own; every release speaking `grid/1` interoperates, and a new protocol comes only with a new major release.

---

## 1. Mental Model

grid sends jobs to machines and brings the results back. What a node can run is fixed by the node: its config defines named **cmds**. A **job** is a TOML file that pushes files in, runs some of those cmds as steps (it can only add `args` and `env`, never new commands), and pulls files back. The **hub** takes the job, picks a **node** that offers the job's **tag** and has a free **slot**, and runs it there in a **space** (the job's own folder).

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
| slot  | room for one job on a node                                                                |

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
- Each side holds the **other** side's public key, always, with or without `signal`: the hub needs it to dial the node; the node accepts only the hub's key. A `.key` never leaves its machine.
- In configs, `key` is the public key itself or a path to its file; `private` is a path only.
- `grid key new` refuses to overwrite.

## 3. Node Config — `/etc/grid/node.toml`

```toml
private = "/etc/grid/node.key"   # required
tags = ["build"]                 # required
slots = 4                        # default 1
expires = "10m"                  # idle job cleanup; "0" = never (default)
history = "30d"                  # "0" = forever (default)
signal = true                    # send beat + state changes to the hub; default false
port = 9420                      # default
data = "/var/lib/grid"           # default

[hub]                            # required: who may call this node
ip = "10.0.0.1"
key = "/etc/grid/hub.pub"
port = 9420                      # where the hub listens, with signal

[cmds.build]
exec = ["make", "-j8"]
timeout = "1h"                   # "0" = never
```

## 4. Hub Config — `/etc/grid/hub.toml`

```toml
private = "/etc/grid/hub.key"    # required
signal = true                    # listen for the nodes' beat and post; default false
port = 9420                      # where it listens, with signal
history = "30d"                  # finished jobs and files; "0" = forever (default)
data = "/var/lib/grid/hub"       # default

[[nodes]]                        # at least one
tags = ["build"]
ip = "10.0.0.2"
key = "/etc/grid/node.pub"
port = 9420
```

**TOML rules that bite:**

- Top-level keys must come **before** the first `[table]`; a key after `[hub]` belongs to `[hub]`.
- Durations are **strings**: `"10m"`, `"30d"`, `"0"`. A bare number (`expires = 600`) is rejected.
- Unknown keys are errors, named with their line: `unknown keys: retry (line 2)`.

## 5. Job File — any name, TOML

```toml
tag = "build"
retries = 2                      # if the node is lost, run it again up to 2 more times

[[push]]                         # repeatable; before the steps
from = "./src"                   # local, relative to the job file
to = "src"                       # in the space; default the space itself

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
- Pushes are packed at submit; the local files are not needed later.

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
grid node status | jobs [--history] | job <id> | tail <id> [--after N]

# hub; submit prints the id, and --wait exits 0 only if the job is done
grid hub run [--config /etc/grid/hub.toml]
grid hub submit <job.toml> [--wait]
grid hub status | nodes | jobs [--history] | job <id> | tail <id> [--after N]
grid hub pull <id> <dir>
grid hub kill <id> | drop <id>

# test kit
grid test node | hub | vectors

# this binary's version and protocol: grid v0.1.0 (protocol grid/1)
grid version
```

Flags: `--json`, `--watch`. Every command but `run` talks to the daemon on the same machine.

## 8. Docker

```dockerfile
FROM ghcr.io/diphyx/grid-node
COPY cmds/ /opt/cmds/
```

- Config and keys in `/etc/grid`; state in `/var/lib/grid`; runs as uid `100` (`grid`): `chown 100` mounted private keys.
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

## 11. Setups

| Setup                            | IPs in the configs     | Open TCP `9420`                                                                       |
| -------------------------------- | ---------------------- | ------------------------------------------------------------------------------------- |
| one machine, containers          | the container IPs      | nothing; they share a Docker network                                                  |
| local network, several machines  | private IPs (LAN, VPC) | on each node to the hub; on the hub to the nodes with `signal`                        |
| public network, several machines | public IPs             | the same, limited to the other side's IP; a node behind NAT needs a reachable address |

## 12. Test Kit

| Command             | Checks                                                                                          |
| ------------------- | ----------------------------------------------------------------------------------------------- |
| `grid test node`    | a node: 76 checks; needs the kit cmds (`pkg/kit/cmds/node.toml`), `tags = ["kit"]`, `slots = 2` |
| `grid test hub`     | a hub: every frame it sends; that it drops its jobs                                             |
| `grid test vectors` | the golden bytes in `spec/vectors.json`                                                         |

Exit code = failed checks, up to 125. The kit talks to your running program over TCP, so the language does not matter: check your encoder against `spec/vectors.json` first, then point the kit at it. Passing the kit makes any implementation a grid hub or node.

## 13. Common Mistakes

- Swapping private keys instead of `.pub` files.
- `signal = true` on the node but not the hub: the node keeps dialing and nobody answers.
- A cmd that is not in the node's `[cmds.*]`: the job fails at that step, "no cmd".
- Keys after a `[table]` in TOML.
- Editing a config without restarting: both sides read it only at start (jobs survive the restart).
- A job whose tag no node offers: it stays `queued` forever.
- Forgetting `9420` in the firewall: the node must accept the hub's IP; the hub must accept the nodes' IPs only with `signal`.

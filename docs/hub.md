# Hub

> The coordinator: it knows the nodes, takes your jobs, and keeps the results

The hub never runs work itself. For each [job](job.md) it picks a node, pushes the files, asks the node to run the steps, follows the output, pulls the results back, and keeps them for you.

## Config

The hub's config, `/etc/grid/hub.toml`, lists the nodes it may use:

```toml
identity = "/etc/grid/hub.key"      # who it is: its private key

[jobs]
expires = "720h"                    # keep finished jobs this long

[[nodes]]                           # one entry per node
tags = ["build", "test"]            # what that node offers
ip = "10.0.0.2"
trust = "/etc/grid/node.pub"        # whom it accepts: that node's public key, or the key itself
share = true                        # its share is on: prefer it for jobs with a share push

[[nodes]]
tags = ["gpu"]
ip = "52.1.2.3"
port = 9500                         # if not 9420
trust = "/etc/grid/gpu.pub"
```

| Option      | Default              | Description                                                    |
| ----------- | -------------------- | -------------------------------------------------------------- |
| `identity`  | required             | Path to the hub's private key                                  |
| `[[nodes]]` | at least one         | A node it may use: `tags`, `ip`, `trust`, `port`, and `share`  |
| `signal`    | `false`              | Listen for the nodes' heartbeats; each node needs its own `ip` |
| `port`      | `9420`               | The port it listens on, with `signal`                          |
| `socket`    | `/run/grid/hub.sock` | The socket the CLI talks to                                    |

Its state is in two scopes, with the same keys as the node's:

| Scope       | `path`, default             | `limit`, default                                       | `expires`, default                                                             |
| ----------- | --------------------------- | ------------------------------------------------------ | ------------------------------------------------------------------------------ |
| `[jobs]`    | `/var/lib/grid/hub/jobs`    |                                                        | `"720h"`: drop a finished job, its files and inputs, after this; `"0"` = never |
| `[history]` | `/var/lib/grid/hub/history` | `"100MB"`: past it, the oldest entries go; `"0"` = off | `"0"`: drop entries older than this                                            |

- A node's `share` is a hint for placing jobs with a [share push](job.md#shared-input). The node's own word wins: its replies to pushes, and its heartbeats with `signal`. A change, or a node that says otherwise than the config, is in the history.
- Under `[jobs] path`, `tars/` keeps every packed input once, for all the jobs that use it; it goes when no job uses it.

Start it:

```bash
grid hub run
```

The config is read at start: after a change, restart the hub. Jobs on disk survive the restart and pick up where they were. To stop trusting a node, remove its `[[nodes]]` entry and restart.

The file follows the same [TOML rules](job.md#the-job-file) as job files.

## CLI

Give the hub a [job](job.md) file; it does the rest. Every command but `run` talks to the hub on the same machine; add `--json` for scripts or `--watch` for a live view.

| Command                      | Options              | Does                                                                                                                             |
| ---------------------------- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `grid hub run`               | `--config <file>`    | Start the hub                                                                                                                    |
| `grid hub submit <job.toml>` | `--wait`, `--repack` | Send a [job](job.md) and print its id; `--wait` follows it and exits `0` only if it succeeds; `--repack` packs every input again |
| `grid hub pull <id> <dir>`   |                      | Copy the files a job brought back                                                                                                |
| `grid hub kill <id>`         |                      | Stop a queued or running job                                                                                                     |
| `grid hub drop <id>`         |                      | Forget a job and delete its files                                                                                                |
| `grid hub status`            |                      | Its key, nodes online, jobs by state                                                                                             |
| `grid hub nodes`             |                      | Every node: tags, online or lost, share, room (known with `signal`), jobs running                                                |
| `grid hub jobs`              |                      | Every job it keeps                                                                                                               |
| `grid hub history`           | `--kind K`           | The event log: `job` (submitted, ended), `node` (lost, online, share), `hub` (started, stopped)                                  |
| `grid hub job <id>`          |                      | One job: its node, steps, state, files                                                                                           |
| `grid hub tail <id>`         | `--after N`          | Follow a job's [events](job.md#watching-a-job)                                                                                   |

## Docker

```dockerfile
FROM ghcr.io/diphyx/grid-hub
COPY hub.toml /etc/grid/hub.toml
```

The image runs `grid hub run`, checks its health with `grid hub status`, and runs as the non-root user `grid` (uid `100`). Keep keys in `/etc/grid`, readable by that user, and mount a volume at `/var/lib/grid` for state: the defaults are under `/var/lib/grid/hub`.

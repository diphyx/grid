# Hub

> The coordinator: it knows the nodes, takes your jobs, and keeps the results

The hub never runs work itself. For each [job](job.md) it picks a node, pushes the files, asks the node to run the steps, follows the output, pulls the results back, and keeps them for you.

## Config

The hub's config, `/etc/grid/hub.toml`, lists the nodes it may use:

```toml
private = "/etc/grid/hub.key"   # its own private key
history = "30d"                 # keep finished jobs this long

[[nodes]]                       # one entry per node
tags = ["build", "test"]        # what that node offers
ip = "10.0.0.2"
key = "/etc/grid/node.pub"      # that node's public key, or the key itself

[[nodes]]
tags = ["gpu"]
ip = "52.1.2.3"
port = 9500                     # if not 9420
key = "/etc/grid/gpu.pub"
```

| Option      | Default             | Description                                                      |
| ----------- | ------------------- | ---------------------------------------------------------------- |
| `private`   | required            | Path to the hub's private key                                    |
| `[[nodes]]` | at least one        | A node it may use: `tags`, `ip`, `key`, and `port`               |
| `history`   | `"0"`               | How long finished jobs and their files are kept; `"0"` = forever |
| `signal`    | `false`             | Listen for the nodes' heartbeats; each node needs its own `ip`   |
| `port`      | `9420`              | The port it listens on, with `signal`                            |
| `data`      | `/var/lib/grid/hub` | Where it keeps jobs, their events, and pulled files              |

Start it:

```bash
grid hub run
```

The config is read at start: after a change, restart the hub. Jobs on disk survive the restart and pick up where they were. To stop trusting a node, remove its `[[nodes]]` entry and restart.

The file follows the same [TOML rules](job.md#the-job-file) as job files.

## CLI

Give the hub a [job](job.md) file; it does the rest. Every command but `run` talks to the hub on the same machine; add `--json` for scripts or `--watch` for a live view.

| Command                      | Options           | Does                                                                                         |
| ---------------------------- | ----------------- | -------------------------------------------------------------------------------------------- |
| `grid hub run`               | `--config <file>` | Start the hub                                                                                |
| `grid hub submit <job.toml>` | `--wait`          | Send a [job](job.md) and print its id; `--wait` follows it and exits `0` only if it succeeds |
| `grid hub pull <id> <dir>`   |                   | Copy the files a job brought back                                                            |
| `grid hub kill <id>`         |                   | Stop a queued or running job                                                                 |
| `grid hub drop <id>`         |                   | Forget a job and delete its files                                                            |
| `grid hub status`            |                   | Its key, nodes online, jobs by state                                                         |
| `grid hub nodes`             |                   | Every node: tags, online or lost, free slots (known with `signal`), jobs running             |
| `grid hub jobs`              | `--history`       | Current jobs, or finished ones                                                               |
| `grid hub job <id>`          |                   | One job: its node, steps, state, files                                                       |
| `grid hub tail <id>`         | `--after N`       | Follow a job's [events](job.md#watching-a-job)                                               |

## Docker

```dockerfile
FROM ghcr.io/diphyx/grid-hub
COPY hub.toml /etc/grid/hub.toml
```

The image runs `grid hub run`, checks its health with `grid hub status`, and runs as the non-root user `grid` (uid `100`). Keep keys in `/etc/grid`, readable by that user, and mount a volume at `/var/lib/grid` for state.

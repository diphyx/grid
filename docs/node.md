# Node

> The worker: it offers a fixed set of cmds and runs jobs with them

A node works for one hub. It runs each job in its own space, using only the cmds its config defines, and reports everything as it happens. It does not decide anything: it runs, reports, and cleans up.

## Config

The node's config, `/etc/grid/node.toml`, names its hub and the cmds it offers:

```toml
private = "/etc/grid/node.key"   # its own private key
tags = ["build"]                 # the kinds of work it does
slots = 4                        # jobs at once
expires = "10m"                  # drop a job left idle this long

[hub]                            # the one hub allowed to send it jobs
ip = "10.0.0.1"
key = "/etc/grid/hub.pub"

[cmds.build]                     # a cmd it offers, named "build"
exec = ["make", "-j8"]
timeout = "1h"

[cmds.test]
exec = ["/opt/cmds/test"]
```

| Option          | Default         | Description                                                                     |
| --------------- | --------------- | ------------------------------------------------------------------------------- |
| `private`       | required        | Path to the node's private key                                                  |
| `tags`          | required        | The kinds of work it does                                                       |
| `[hub]`         | required        | The hub allowed to send it jobs: `ip`, `key`, and `port`                        |
| `[cmds.<name>]` |                 | A cmd it offers: `exec` is the command line, `timeout` its limit                |
| `slots`         | `1`             | Jobs at once; when all are taken, new jobs wait on the hub                      |
| `expires`       | `"0"`           | Delete a job left idle this long; `"0"` = never; a running cmd is never cut off |
| `history`       | `"0"`           | How long finished cmds stay listed; `"0"` = forever                             |
| `signal`        | `false`         | Connect back to the hub to send heartbeats and step updates                     |
| `port`          | `9420`          | The port it listens on                                                          |
| `data`          | `/var/lib/grid` | Where it keeps spaces, events, and history                                      |

Open TCP `9420` to the hub's IP, and start it:

```bash
grid node run
```

The file follows the same [TOML rules](job.md#the-job-file) as job files. The config is read at start: after a change, restart the node. Its jobs are kept; a cmd that was running ends `failed` with "node restarted", which fails its job.

## Cmds

A cmd is any program, under a name. A step calls it by that name and can only add to it:

```
┌──────────────────┐     ┌─────────────────┐     ┌────────────────┐
│ node.toml        │     │ job step        │     │ what runs      │
│                  │     │                 │     │                │
│ [cmds.build]     │  +  │ cmd  = "build"  │  ═▶ │ make -j8 linux │
│ exec = make -j8  │     │ args = [linux]  │     │ in the space,  │
└──────────────────┘     │ env  = CC=clang │     │ with CC=clang  │
                         └─────────────────┘     └────────────────┘
```

Nothing outside `[cmds.*]` can run, so whoever runs the node decides what may run on it.

### How a Cmd Runs

When a step runs, the node starts its cmd:

- **in the job's space**, so the pushed files are right there, and what it writes there can be pulled;
- **with the command line** from `exec`, plus the step's `args`;
- **with the environment** of the node, plus the step's `env`, plus these three:

| Variable      | Holds                                                |
| ------------- | ---------------------------------------------------- |
| `GRID_JOB`    | the job's id on this node (not the hub's)            |
| `GRID_SPACE`  | the space's full path                                |
| `GRID_EVENTS` | a file for [reporting progress](#reporting-progress) |

Everything it prints on stdout and stderr goes to the hub, line by line. It runs as the node's user, in its own process group: when it ends or is stopped, whatever it started stops too. On Windows a job object does the same, and stopping is immediate: there is no SIGTERM. The isolation is the node itself: run nodes in containers or VMs.

### How a Step Ends

| When the cmd                                          | The step is                                                      |
| ----------------------------------------------------- | ---------------------------------------------------------------- |
| exits with `0`                                        | `done`                                                           |
| exits with anything else, or cannot start             | `failed`, with its last stderr line, or the error, as the reason |
| is killed, runs past `timeout`, or the job is dropped | `stopped`: SIGTERM, then SIGKILL after 10 s                      |

### Reporting Progress

A cmd has two ways to send things to the hub:

| The cmd writes to          | It arrives as                       | In `grid hub tail`              |
| -------------------------- | ----------------------------------- | ------------------------------- |
| stdout or stderr           | text, one line at a time            | `out  built linux`, `err  boom` |
| the file at `$GRID_EVENTS` | an event: a name and optional pairs | `evt  progress percent=50`      |

The first is for people. The second is for programs: a fact like "50% done", which a progress bar can use without parsing text. It is a separate file, not stdout, so it never mixes with the output.

Each line in the file is one event: a name in brackets, then optional `key=value` pairs.

```bash
#!/bin/sh
# /opt/cmds/test
echo "[started]" >> "$GRID_EVENTS"
echo "[progress] percent=50" >> "$GRID_EVENTS"
echo '[stage] name=test note="half way there"' >> "$GRID_EVENTS"
```

| Part     | Rule                                                                                                                        |
| -------- | --------------------------------------------------------------------------------------------------------------------------- |
| `[name]` | required, first on the line: lowercase letters, digits, `.`, `_`, `-`, up to 64 characters                                  |
| pairs    | optional, separated by spaces; keys follow the name rule; a value with spaces goes in `"quotes"`, with `\"` and `\\` inside |
| a line   | one event, up to 8 KiB                                                                                                      |

At the hub, the event's type is the name with `evt.` in front, so a cmd's names never clash with grid's own; the pairs are its data, always as text. A program reading `grid hub tail --json` gets:

```json
{"seq":7,"time":1762531469651,"type":"evt.progress","data":{"percent":"50"}}
```

A line that is not an event, like one without `[name]`, arrives as an `err` line instead, so a mistake shows up in the output and the job carries on. Events arrive within a moment of being written, and always before the step ends.

Results do not go here: what a job produces goes in files, which you [pull](job.md#the-job-file).

## CLI

Every command but `run` talks to the node on the same machine, and uses the node's own job ids, which differ from the hub's.

| Command               | Options           | Does                                   |
| --------------------- | ----------------- | -------------------------------------- |
| `grid node run`       | `--config <file>` | Start the node                         |
| `grid node status`    |                   | Its tags, slots, uptime, hub connected |
| `grid node jobs`      | `--history`       | Current jobs, or finished cmds         |
| `grid node job <id>`  |                   | One job: its cmds, files, state        |
| `grid node tail <id>` | `--after N`       | Follow a job's events                  |

## Docker

A node image is the base image plus its cmds:

```dockerfile
FROM ghcr.io/diphyx/grid-node
COPY cmds/ /opt/cmds/
COPY node.toml /etc/grid/node.toml
```

The image runs `grid node run`, checks its health with `grid node status`, and runs as the non-root user `grid` (uid `100`), which the cmds run as too. Keep keys in `/etc/grid`, readable by that user, and mount a volume at `/var/lib/grid` for state.

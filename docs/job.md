# Job

> A request to a node: push files in, run some of its cmds, pull files back

The job is what the hub and a node share. It is a request, not code: the node decides in advance what it can run, and the job only picks from that.

## Offered, Then Asked For

The node's config names its **cmds**:

```toml
# node.toml: what the node offers
[cmds.build]
exec = ["make", "-j8"]
```

A job asks for them by name, adding `args` and `env`:

```toml
# the job: what it asks for
[[steps]]
cmd = "build"
args = ["linux"]     # the node runs: make -j8 linux
```

A step can name only a cmd the node offers. There is no way to run anything else.

## Files In, Work, Files Out

Every job runs in its own folder on the node, its **space**. Files move in and out of it in three parts, always in this order:

```
            hub                                       node
  ┌──────────────────────┐                ┌──────────────────────────┐
  │ build.toml           │                │ the job's space          │
  │                      │                │                          │
  │ [[push]]  ./src      │ ── files in ─▶ │ src/                     │
  │                      │                │                          │
  │ [[steps]] build      │ ──── run ────▶ │ make -j8 linux           │
  │   args = [linux]     │                │   with CC=clang          │
  │   env  = CC=clang    │ ◀── output ─── │                          │
  │                      │                │                          │
  │ [[pull]]  out        │ ◀─ files out ─ │ out/                     │
  └──────────────────────┘                └──────────────────────────┘
```

| Part      | When             | What happens                                                 |
| --------- | ---------------- | ------------------------------------------------------------ |
| **push**  | before the steps | Your files are copied from the hub into the space            |
| **steps** | in order         | The node runs its cmds in the space, on those files          |
| **pull**  | after the steps  | The files the job names are copied from the space to the hub |

Push brings the inputs, the cmds work on them in place, and pull takes the outputs. When the job ends, the space is deleted.

- Pushed files are packed when you submit, so the job no longer needs them on your side. The hub keeps each packed input once and knows it again by its files' names, sizes, and times: an input that did not change is not packed again. `grid hub submit --repack` packs everything again.
- A broken transfer resumes where it stopped; a checksum confirms the whole.
- A path that would leave the space (`../`, absolute, a symlink out) is rejected.
- Pulled files stay on the hub until `grid hub drop`, or until the hub's `[jobs] expires` has passed.

## The Job File

A job is a TOML file, with any name. It reads in the order the job runs: push, steps, pull.

```toml
tag = "build"                    # run on a node that offers "build"
retries = 2                      # if the node goes away, run it again up to 2 more times

[[push]]                         # files in, before the steps
from = "./src"                   # on your side, relative to this file
to = "src"                       # in the space; the space itself if left out

[[steps]]                        # the node's "build" cmd
cmd = "build"
args = ["linux"]                 # added to its command line
env = { CC = "clang" }           # added to its environment

[[pull]]                         # files out, after the steps
path = "out"                     # in the space; a file or a folder
```

Each part can repeat: several `[[push]]`, `[[steps]]` (run in order), or `[[pull]]` entries.

### Shared Input

A large input that many jobs use, like a model or a dataset, can be a **share**: the node keeps it for the jobs after, so it travels to each node once, not once per job.

```toml
[[push]]
from = "./model"
to = "model"                     # required: the share is linked here
share = true
```

- The node keeps it read only and links it at `to`; a cmd reads it there like any folder, and must not write to it.
- The same content is the same share, from any job; a changed input is a new one.
- `to` cannot be the space itself. No other push or pull may be at `to` or under it, and a pull may not hold it: such a job is rejected at submit.
- A node with its share off, or full, takes it as a normal push; the job runs either way. `grid hub tail` shows how each push went: `push model: hit` (the node had it), `installed` (sent, and kept), or `normal`.

> **TOML rules,** for job files and both configs: an unknown key is an error that names the key and its line; top-level keys come before the first `[table]`; durations are strings, like `"10m"`, `"30d"`, or `"0"`.

Submit it, and copy the results out when it ends:

```bash
# follow it; exit 0 if it succeeds
grid hub submit build.toml --wait

# copy the files it brought back
grid hub pull <id> ./result
```

## Job States

```
 submit
   │
   ▼
┌────────┐  a node with the tag   ┌─────────┐
│ queued │ ─ has room ──────────▶ │ running │ ◀─┐  node lost, retries left:
└────────┘                        └────┬────┘ ──┘  start over on another node
                                       │
        ╭──────────────────────────────┼──────────────────────╮
        │ every step done              │ a step failed, or    │ grid hub kill
        ▼                              ▼ the node was lost,   ▼
                                         no retries left
    ┌──────┐                      ┌────────┐             ┌─────────┐
    │ done │                      │ failed │             │ stopped │
    └──────┘                      └────────┘             └─────────┘
```

| State     | Means                                                                   |
| --------- | ----------------------------------------------------------------------- |
| `queued`  | No node with the tag has room yet; the hub tries again every 2 s        |
| `running` | On a node: pushing, running the steps, or pulling                       |
| `done`    | Every step succeeded and the files were pulled                          |
| `failed`  | A step failed or ran past its `timeout`, or the node went away for good |
| `stopped` | Killed with `grid hub kill`                                             |

## Watching a Job

Everything a job does is a numbered event. `grid hub tail <id>` shows them as they come; `--after N` resumes after event `N`.

```
19:34:29.631     1 job  queued
19:34:29.633     3 hub  attempt 1 on 10.0.0.2:9420
19:34:29.640     6 cmd  running build
19:34:29.651     9 out  built linux
19:34:29.660    11 cmd  done build
19:34:29.663    13 job  done
```

| Prefix        | Shows                                            |
| ------------- | ------------------------------------------------ |
| `job`         | the job's state                                  |
| `cmd`         | a step's cmd started or ended                    |
| `hub`         | what the hub did                                 |
| `out` / `err` | a line the cmd printed                           |
| `evt`         | an event the cmd sent itself: its name and pairs |

A cmd can also send its own events, like progress; see [Reporting Progress](node.md#reporting-progress).

## When Things Fail

Nodes report; the hub decides.

| When                   | Then                                                                            |
| ---------------------- | ------------------------------------------------------------------------------- |
| a step fails           | the rest are skipped, the files are still pulled, the job is `failed`           |
| the node goes away     | the job starts over, on another node if one is free, up to `retries` more times |
| no node offers the tag | it waits until one does: add one to the hub's config and restart the hub        |
| a connection drops     | only that call is sent again; the job keeps running                             |
| the hub restarts       | running jobs are picked up again, where they were                               |
| the node restarts      | a cmd that was running ends `failed`, "node restarted"                          |

```
┌─────┐  job   ┌────────┐
│ hub │ ─────▶ │ node-1 │  ✗ gone: 15 s without a heartbeat (signal),
└─────┘        └────────┘    or 30 s of failed calls
   │
   │  retries left: start over on another node
   ▼
┌────────┐
│ node-2 │  ✓ done
└────────┘
```

To see why a job failed:

```bash
# each step's state, and the failed step's message: its last stderr line, or why it could not run
grid hub job <id>

# everything it printed and did, in order
grid hub tail <id>
```

To stop one yourself: `grid hub kill <id>` stops it now, even mid-transfer (`stopped`); `grid hub drop <id>` forgets it and deletes its files, killing it first if it runs.

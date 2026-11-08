# grid

> Send jobs to machines you control, and get the results back

grid has two sides, talking over one protocol:

- the **hub** takes your jobs, picks where each one runs, and keeps the results;
- a **node** is a machine, container, or VM that offers a fixed set of commands and runs jobs with them.

```
               job file
                  │
                  ▼
        ┌───────────────────┐   jobs, files    ┌────────────────────┐
        │        hub        │ ───────────────▶ │        node        │
        │                   │                  │                    │
        │ picks a node,     │                  │ offers cmds,       │
        │ follows the job,  │ ◀─────────────── │ runs the job,      │
        │ keeps the results │ output, results  │ reports as it goes │
        └───────────────────┘                  └────────────────────┘
```

## Why

You have machines that should do certain work for you: build, test, render, train, convert. You want to send them that work from somewhere else, without handing out shell access or running a CI server. grid does exactly that.

- **Fixed work, not a shell.** A node runs only the commands its own config names; a job can add `args` and `env`, never new commands. Compared with SSH, nobody gets a shell.
- **One port, no certificates.** Hub and node talk over one TCP port, `9420`, encrypted end to end. Each side trusts only the other's public key, from a known IP; no domains, no CA, no shared secret.
- **Files in, results out.** A job pushes your files to the node and pulls the results back to the hub. You watch the output live while it runs.
- **It keeps going.** A lost node means the job runs again on another one; a broken transfer resumes; a restarted hub picks its jobs up where they were.
- **One binary, or an image.** `grid` runs either side, as a binary or as a Docker image; anything else that speaks the protocol works with them too.

| Good fit                                                                          | Not a fit                                                         |
| --------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| builds, tests, conversions, renders, model runs: work that starts, runs, and ends | long-running services: a job is meant to finish                   |
| sending work to machines you own, on any network                                  | an interactive shell, or running arbitrary commands               |
| a few to many workers, each offering what it is good at                           | scheduling on its own: there is no cron; call `submit` from yours |
| keeping each job in its own clean folder                                          | splitting one job across many nodes: a job runs on one            |

## How

A **job** is a request, not code: it picks from the node's **cmds**, pushes your files in, runs those cmds on them, and pulls the results back. The [Job](job.md) page shows how.

1. You submit a job to the hub.
2. The hub picks a node that offers the job's **tag** (a kind of work, like `build`) and has room for one more job, under its `[jobs] limit`.
3. The node makes a **space**, the job's own folder, and the hub pushes the files in.
4. The node runs the job's **steps**, its chosen cmds, one after another, sending each line of output as it goes.
5. The hub pulls the files back, and the node deletes the space.

Nodes do the work; the hub makes the decisions.

## Where

The same protocol works in every setup; only the IPs and what you open change.

| Setup                            | IPs in the configs | Open TCP `9420`                                                |
| -------------------------------- | ------------------ | -------------------------------------------------------------- |
| one machine, containers          | container IPs      | nothing; they share a Docker network                           |
| local network, several machines  | private IPs        | on each node to the hub; on the hub to the nodes with `signal` |
| public network, several machines | public IPs         | the same, but only to the other side's IP                      |

Drawings and config for each: [Setups](setups.md).

## Install

One binary, `grid`, runs either side, on macOS, Linux, and Windows, amd64 and arm64:

| With                                                         | Command                                 | Updates                       |
| ------------------------------------------------------------ | --------------------------------------- | ----------------------------- |
| Homebrew (macOS, Linux)                                      | `brew install diphyx/tap/grid`          | `brew upgrade grid`           |
| a [release](https://github.com/diphyx/grid/releases) archive | unpack it and put `grid` on your `PATH` | a newer archive               |
| Docker                                                       | `ghcr.io/diphyx/grid-hub`, `grid-node`  | tags `latest`, `1.2.3`, `1.2` |

Check it with `grid version`, which prints the release and the protocol it speaks:

```bash
grid version
# grid v1.2.3 (protocol grid/1)
```

## Start

Each side makes its own key pair:

```bash
# on the hub: hub.key (0600) and hub.pub; never overwrites
grid key new /etc/grid/hub

# print the public key again
grid key pub /etc/grid/hub.key
```

Make a pair on each side, and swap only the `.pub` files. Both sides always need the other's, with or without `signal`:

| Side | Holds         | Because                                                    |
| ---- | ------------- | ---------------------------------------------------------- |
| hub  | each node.pub | it dials the node, and must know its key before connecting |
| node | hub.pub       | it accepts only that key, so nobody else can send it jobs  |

Then write a [job](job.md), set up the [hub](hub.md) and a [node](node.md), and submit it:

```bash
grid hub submit build.toml --wait
```

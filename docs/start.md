# Get Started

> From nothing to a first job's results: install, keys, a node, a hub, a job

You need two places: one for the hub, one for the node. Two machines, or two containers on one machine; [Deploy](deploy.md) has a ready `compose.yaml` for that. Below, the hub is `10.0.0.1` and the node `10.0.0.2`.

## 1. Install

One binary, `grid`, runs either side, on macOS, Linux, and Windows, amd64 and arm64. Install it on both:

| With                                                         | Command                                 | Updates                 |
| ------------------------------------------------------------ | --------------------------------------- | ----------------------- |
| Homebrew (macOS, Linux)                                      | `brew install diphyx/tap/grid`          | `brew upgrade grid`     |
| a [release](https://github.com/diphyx/grid/releases) archive | unpack it and put `grid` on your `PATH` | a newer archive         |
| Docker                                                       | `ghcr.io/diphyx/grid-hub`, `grid-node`  | tag `2`, the generation |

Check it with `grid version`, which prints the release and the protocol it speaks:

```bash
grid version
# grid v2.1.0 (protocol grid/2)
```

Updates within a generation, `2.x.y`, are drop-in. A new major is a fresh install, with a fresh config: see [Versions](versions.md).

## 2. Make the Keys

Each side makes its own key pair:

```bash
# on the hub: hub.key (0600) and hub.pub; never overwrites
grid key new /etc/grid/hub

# on the node: node.key (0600) and node.pub
grid key new /etc/grid/node

# print a public key again
grid key pub /etc/grid/hub.key
```

Then swap only the `.pub` files: copy `hub.pub` to the node, and `node.pub` to the hub, both into `/etc/grid`. Both sides always need the other's, with or without `signal`:

| Side | Holds         | Because                                                    |
| ---- | ------------- | ---------------------------------------------------------- |
| hub  | each node.pub | it dials the node, and must know its key before connecting |
| node | hub.pub       | it accepts only that key, so nobody else can send it jobs  |

A `.key` never leaves its machine.

## 3. Set Up the Node

The node's config names its hub and the cmds it offers; here, one cmd, `build`:

```toml
# /etc/grid/node.toml
grid = 2
identity = "/etc/grid/node.key"
tags = ["build"]

[[hubs]]
ip = "10.0.0.1"
trust = "/etc/grid/hub.pub"

[cmds.build]
exec = ["make", "-j8"]
```

Open TCP `9420` on the node to the hub's IP, and start it:

```bash
grid node run
```

Every option, and how a cmd runs: [Node](node.md).

## 4. Set Up the Hub

The hub's config lists the nodes it may use, with the tags they offer:

```toml
# /etc/grid/hub.toml
grid = 2
identity = "/etc/grid/hub.key"

[[nodes]]
tags = ["build"]
ip = "10.0.0.2"
trust = "/etc/grid/node.pub"
```

```bash
grid hub run
```

Every option, and the hub's commands: [Hub](hub.md).

## 5. Write a Job

A job file asks for a node with the `build` tag, sends `./src`, runs the node's `build` cmd on it, and brings `./out` back:

```toml
# build.toml
tag = "build"

[[push]]
from = "./src"

[[steps]]
cmd = "build"
args = ["linux"]

[[pull]]
path = "out"
```

On the node, this runs `make -j8 linux` in the job's own folder. Every part of a job file: [Job](job.md).

## 6. Run It

On the hub, submit it and follow it; `--wait` exits `0` only if the job succeeds:

```bash
grid hub submit build.toml --wait
```

Then copy the files it brought back:

```bash
grid hub pull <id> ./result
```

`grid hub tail <id>` shows everything the job did, in order; `grid hub job <id>` shows its steps and, for a failed one, why.

## Next

| To                                          | Read                    |
| ------------------------------------------- | ----------------------- |
| learn the job file, its states and failures | [Job](job.md)           |
| place jobs on several nodes, by tag         | [Hub](hub.md)           |
| offer cmds, report progress, keep shares    | [Node](node.md)         |
| run it on one machine, a LAN, or the web    | [Deploy](deploy.md)     |
| write your own hub or node                  | [Protocol](protocol.md) |

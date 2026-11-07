# grid

Send jobs to machines you control, and get the results back.

**Docs: [diphyx.github.io/grid](https://diphyx.github.io/grid/)** · [Overview](docs/README.md) · [Job](docs/job.md) · [Hub](docs/hub.md) · [Node](docs/node.md) · [Setups](docs/setups.md) · [Protocol](docs/protocol.md) · [Agents](docs/AGENTS.md)

## 1. What It Is

grid has two sides that talk over one protocol. A **hub** takes your jobs, picks where each one runs, and keeps the results. A **node** is a machine, container, or VM that offers a fixed set of commands and runs jobs with them. They talk over one TCP port, `9420`, encrypted with a key pair on each side.

```bash
go install github.com/diphyx/grid@latest

# each side makes its own key pair and trusts the other's .pub
grid key new /etc/grid/hub
```

## 2. Job

The job is what hub and node share. What a node can run is fixed by the node: its config names a few **cmds**, like `build` → `make -j8`. A job cannot bring its own commands; it is a request to run some of those cmds, by name, with its own `args` and `env`, on its own files:

| Part      | Direction   | What it does                                                                             |
| --------- | ----------- | ---------------------------------------------------------------------------------------- |
| **push**  | hub → node  | Before the steps, your files are copied into the job's own folder on the node            |
| **steps** | on the node | The node runs its cmds one after another in that folder, with the job's `args` and `env` |
| **pull**  | node → hub  | After the steps, the files the job names are copied back to the hub                      |

```toml
# build.toml: send ./src, run the node's "build" cmd, bring back ./out
tag = "build"

[[push]]
from = "./src"

[[steps]]
cmd = "build"
args = ["linux"]

[[pull]]
path = "out"
```

## 3. Hub

```toml
# /etc/grid/hub.toml
private = "/etc/grid/hub.key"

[[nodes]]
tags = ["build"]
ip = "10.0.0.2"
key = "/etc/grid/node.pub"
```

```bash
grid hub run

# run the job and follow it; exits 0 if it succeeds
grid hub submit build.toml --wait

# copy the files it brought back
grid hub pull <id> ./result
```

## 4. Node

```toml
# /etc/grid/node.toml
private = "/etc/grid/node.key"
tags = ["build"]

[hub]
ip = "10.0.0.1"
key = "/etc/grid/hub.pub"

[cmds.build]
exec = ["make", "-j8"]
```

```bash
grid node run
```

## 5. Reference

| Topic                        | Covers                                                                                               |
| ---------------------------- | ---------------------------------------------------------------------------------------------------- |
| [Setups](docs/setups.md)     | One machine with containers, a local network, or the public internet: what changes and what to open  |
| [Protocol](docs/protocol.md) | Your own hub or node in any language: the protocol, [`spec/SPEC.md`](spec/SPEC.md), and the test kit |
| [Agents](docs/AGENTS.md)     | One file with the whole model, for AI coding agents                                                  |

## Develop

| Path         | Is                                                                            |
| ------------ | ----------------------------------------------------------------------------- |
| `spec/`      | the protocol, exact (`SPEC.md`); the golden bytes (`vectors.json`)            |
| `main.go`    | `main`, nothing else                                                          |
| `cmd/`       | the `grid` command: hub, node, key, test                                      |
| `pkg/wire/`  | the protocol in Go: keys, frames, Noise, envelopes, bodies, tar, event log    |
| `pkg/node/`  | the node: package and daemon                                                  |
| `pkg/hub/`   | the hub: package and daemon                                                   |
| `pkg/kit/`   | the test kit: a fake hub, a fake node, the kit cmds                           |
| `Dockerfile` | targets `node` and `hub`                                                      |
| `docs/`      | the site for GitHub Pages: Markdown pages, rendered by Docsify in the browser |

Each package's `doc.go` maps its files.

| Tests       | Cover                                                                                                                         |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `cmd/`      | `grid` as real processes: keys, every hub and node command, exit codes, bad input                                             |
| `pkg/wire/` | handshake, keys, envelopes, paths, tar                                                                                        |
| `pkg/node/` | the whole kit against the real node; restart; expiry; posts after a restart                                                   |
| `pkg/hub/`  | a run; failover; a failed step; kill and drop; kill mid-push; a lost node; a hub restart; the hub against the kit's fake node |
| `pkg/kit/`  | every node check against the kit's fake node; the vectors are current                                                         |

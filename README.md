# GRID

**Generic Runtime Intent-Driven**

Send jobs to machines you control, and get the results back.

**Docs: [diphyx.github.io/grid](https://diphyx.github.io/grid/)** · [Overview](docs/README.md) · [Get Started](docs/start.md) · [Job](docs/job.md) · [Hub](docs/hub.md) · [Node](docs/node.md) · [Deploy](docs/deploy.md) · [Protocol](docs/protocol.md) · [Versions](docs/versions.md) · [Agents](docs/AGENTS.md)

## 1. What It Does

GRID has two sides that talk over one protocol. A **hub** takes your jobs, picks where each one runs, and keeps the results. A **node** is a machine, container, or VM that offers a fixed set of commands and runs jobs with them.

```
   you: "build ./src on a build node"
              │
              ▼
            hub ──► node with the tag ──► push files ──► run its cmds ──► pull results
                                                                               │
              ◄────────────────────────── results kept on the hub ◄────────────┘
```

A job cannot bring its own commands. It is a request to run some of the node's **cmds**, by name, with its own `args` and `env`, on its own files:

| Part      | Direction   | What it does                                                                                                                         |
| --------- | ----------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| **push**  | hub → node  | Before the steps, your files are copied into the job's own folder on the node; a `share` push is kept on the node for the jobs after |
| **steps** | on the node | The node runs its cmds one after another in that folder, with the job's `args` and `env`                                             |
| **pull**  | node → hub  | After the steps, the files the job names are copied back to the hub                                                                  |

For jobs that take input, run for minutes or hours, report as they go, and give files back. Between SSH and a cluster:

|               | SSH, Ansible       | **GRID**                  | Slurm, Kubernetes   |
| ------------- | ------------------ | ------------------------- | ------------------- |
| **What runs** | any command        | the node's fixed commands | any script or image |
| **Files**     | you copy them      | in and out with the job   | shared storage      |
| **Where**     | logged-in machines | containers, LAN, internet | the cluster         |
| **To set up** | SSH access         | one binary per side       | a cluster           |

More: [Where It Sits](https://diphyx.github.io/grid/#/?id=where-it-sits).

## 2. A Taste

Install, and give each side its own key pair; each trusts the other's `.pub`. More ways to install, like release archives and Docker images: [Get Started](docs/start.md#1-install).

```bash
brew install diphyx/tap/grid

grid key new /etc/grid/hub    # on the hub
grid key new /etc/grid/node   # on the node
```

The node names what it can run:

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

The hub knows its nodes:

```toml
# /etc/grid/hub.toml
grid = 2
identity = "/etc/grid/hub.key"

[[nodes]]
tags = ["build"]
ip = "10.0.0.2"
trust = "/etc/grid/node.pub"
```

A job asks for a tag and picks the node's cmds:

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

```bash
grid node run                       # on the node
grid hub run                        # on the hub

grid hub submit build.toml --wait   # run the job and follow it; exits 0 if it succeeds
grid hub pull <id> ./result         # copy the files it brought back
```

## 3. How It Is Built

- **Fixed work, not a shell.** A node runs only the cmds its own config names; a job adds `args` and `env`, never new commands.
- **One port, no certificates.** Hub and node talk over TCP `9420`, encrypted end to end; each side trusts only the other's public key.
- **It keeps going.** A lost node means the job runs again on another one; a broken transfer resumes; a restarted hub picks its jobs up where they were.
- **Spec first.** [`spec/SPEC.md`](spec/SPEC.md) is the protocol, byte for byte, and the test kit proves it: a hub or node of your own, in any language, works once it passes.
- **Generations.** Each major is a fresh install: its configs say `grid = 2`, its protocol is `grid/2`. Minors and patches are drop-in. [Versions](docs/versions.md)

| Topic                        | Covers                                                                                              |
| ---------------------------- | --------------------------------------------------------------------------------------------------- |
| [Deploy](docs/deploy.md)     | One machine with containers, a local network, or the public internet: what changes and what to open |
| [Protocol](docs/protocol.md) | Your own hub or node in any language: the protocol, the golden bytes, and the test kit              |
| [Agents](docs/AGENTS.md)     | One file with the whole model, for AI coding agents                                                 |

## 4. Develop

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

```bash
make init   # modules and the linter
make lint   # the custom golangci-lint
make test   # go test -race ./...
make docs   # the docs at http://localhost:4000
```

| Tests       | Cover                                                                                                                         |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `cmd/`      | `grid` as real processes: keys, every hub and node command, exit codes, bad input                                             |
| `pkg/wire/` | handshake, keys, envelopes, paths, tar                                                                                        |
| `pkg/node/` | the whole kit against the real node; restart; expiry; posts after a restart                                                   |
| `pkg/hub/`  | a run; failover; a failed step; kill and drop; kill mid-push; a lost node; a hub restart; the hub against the kit's fake node |
| `pkg/kit/`  | every node check against the kit's fake node; the vectors are current                                                         |

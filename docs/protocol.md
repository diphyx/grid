# Protocol

> Your own hub or node, in any language

Follow the protocol below, then pass the test kit. Anything that passes works with every other grid hub and node.

`grid/2` is the wire version, one per generation: every grid 2 release works with every other, and with no other generation ([Versions](versions.md)). `grid version` prints both.

This page gives the picture; [`spec/SPEC.md`](https://github.com/diphyx/grid/blob/HEAD/spec/SPEC.md) defines every byte, and [`spec/vectors.json`](https://github.com/diphyx/grid/blob/HEAD/spec/vectors.json) has sample bytes to check an implementation against.

## A Job on a Node

The hub drives each job with a few calls. On the node, a job is just its space and the cmds run in it.

```
hub                                 node
 │── open {tag} ─────────────────────▶│  makes the space
 │◀─────────────────── {job, expires} │
 │── tail {job} ─────────────────────▶│  events start, and keep coming
 │── push {job} + tar ───────────────▶│  files in
 │◀───────────────────────── {sha256} │
 │── exec {job, cmd, args, env} ─────▶│  runs the cmd on its own
 │◀─────────────── events: log, state │
 │── pull {job, path} ───────────────▶│
 │◀──────── file list + tar, {sha256} │  files out
 │── drop {job} ─────────────────────▶│  deletes the space
```

- One `exec` runs at a time; the next starts only after the last one ended.
- After `exec`, the node runs the cmd on its own; the hub does not have to stay connected.
- `kill` stops the running cmd but keeps the space, so another `exec` can follow.
- A step's state goes `idle` → `running` → `done`, `failed`, or `stopped`.

## Calls

| Hub → node | Does                     | In                                 | Out                         |
| ---------- | ------------------------ | ---------------------------------- | --------------------------- |
| `open`     | gets a job and its space | `tag`                              | `job`, `expires`            |
| `push`     | puts files in            | `job`, `path?`, `id?` + a tar      | `sha256`, `share`           |
| `exec`     | runs a cmd               | `job`, `cmd`, `args?`, `env?`      | —                           |
| `tail`     | follows the events       | `job`, `after?`                    | events                      |
| `stat`     | reads the state          | `job`                              | `cmd`, `state`, `message`   |
| `kill`     | stops the cmd            | `job`                              | —                           |
| `pull`     | takes files              | `job`, `path`, `since?`, `offset?` | file list + a tar, `sha256` |
| `drop`     | ends the job             | `job`                              | —                           |

| Node → hub, with `signal` | Does                        | In                      |
| ------------------------- | --------------------------- | ----------------------- |
| `beat`                    | alive, every 5 s            | `tags`, `free`, `share` |
| `post`                    | a state change, until acked | `job`, `event`          |

Instead of an answer, any call can get a `reject` with a reason: `full` (no room for another job), `running` (a cmd is already running), `unknown` (no such tag, job, or cmd), or `invalid` (bad input).

## On the Wire

```
┌────────────────────────────────────────────────────────────┐
│ envelope   CBOR: {id, call, body}, {id, ok}, {id, data}, … │
├────────────────────────────────────────────────────────────┤
│ frame      u16 length + ciphertext, max 65535 bytes        │
├────────────────────────────────────────────────────────────┤
│ Noise IK   both sides proven, everything encrypted         │
├────────────────────────────────────────────────────────────┤
│ TCP        port 9420                                       │
└────────────────────────────────────────────────────────────┘
```

| Envelope                    | Is            |
| --------------------------- | ------------- |
| `{id, call, body}`          | a call        |
| `{id, ok}` / `{id, reject}` | its reply     |
| `{id, data}` … `{id, end}`  | a stream      |
| `{id, cancel}`              | stop a stream |
| `{ping}` / `{pong}`         | keepalive     |

- Everything goes over TCP port `9420`. Each connection starts with a Noise handshake (`Noise_IK_25519_ChaChaPoly_SHA256`, version `grid/2`) that proves both sides and encrypts the rest.
- Each side accepts connections only from the IPs in its config, then checks the key.
- Only the side that opened a connection makes calls on it. Several calls can run at once, told apart by `id`.
- Files travel as tar streams that come out the same bytes every time, so a broken transfer resumes at an offset, and a `sha256` checks the whole.

## Share Pushes

A push the job marks `share` is kept by the node for the jobs after it, so the same input travels once per node, not once per job. Its `id` is the tar's sha256, first 16 bytes: the same id is the same content.

```
hub                                   node
 │── push {job, path, id} ───────────▶│
 │◀─ data {offset: 0, have: true} ────│  hit: it has the share, linked at path
 │── end {} ─────────────────────────▶│  no bytes
 │◀─ ok {sha256, share: true} ────────│

 │◀─ data {offset, have: false} ──────│  miss: the tar comes as usual
 │── data … end {} ──────────────────▶│
 │◀─ ok {sha256, share: true} ────────│  kept, and linked at path
```

- The node checks a new share's id against the tar's sha256, makes it read only, and links it into the space at `path`: a symlink, or a junction on Windows. A cmd reads it there and must not write to it.
- With its share off, full, or unable to link, the node takes the tar into the space as usual and answers `share: false`; the job runs either way.
- A share stays while a job links it; after that, it goes when unused for the node's `[share] expires`, or, least recently hit first, when the share passes its `limit`.

## Who Dials Whom

The hub always dials the node for job calls; the node dials the hub only for `beat` and `post`, with `signal`. [Setups](setups.md) shows it per setup.

## Test Kit

The test kit checks that a hub or a node follows the protocol. It never looks at your code: it is the `grid` binary, talking to your running program over TCP, the way the other side would. So an implementation in any language, Node.js, Python, Rust, is tested the same way, and once it passes, it works with grid's own hub and node.

| Run                 | The kit acts as | Checks                                                                       |
| ------------------- | --------------- | ---------------------------------------------------------------------------- |
| `grid test vectors` | —               | prints the golden bytes, to check an encoder                                 |
| `grid test node`    | the hub         | a node: 87 checks, every call, state, event, file rule, share, beat and post |
| `grid test hub`     | a node          | a hub: every frame and body it sends; that it drops its jobs                 |

The exit code is the number of failed checks, up to 125; `--json` for CI.

### Your Own Implementation

Work in this order:

1. **Bytes.** Write the encoder from the spec, and check it in your own unit tests against `spec/vectors.json`: fixed keys and the exact bytes they give (handshake, frames, envelopes). No network needed.
2. **Talk.** Run your program and point the kit at it, as below. Each check prints pass or fail, with the section of the spec it tests.
3. **Fix and repeat** until every check passes; then keep the kit in CI.

```sh
# your unit tests, with vectors.json
npm test

# then the kit, against your running node
grid test node --addr 127.0.0.1 --key my-node.pub --private kit.key --limit 2 --share --json
```

### A Node

1. Give the node the kit cmds: copy [`pkg/kit/cmds/`](https://github.com/diphyx/grid/tree/HEAD/pkg/kit/cmds) onto it and append `node.toml` from there to its config, with `KIT` replaced by that folder. Set `tags = ["kit"]`, `[jobs] limit = 2`, and a `[share] limit` for the share checks. These small scripts give known results: `fail` must end `failed`, `nap` must hit its `timeout`, `emit` must send events, `copy` must read a share.
2. Let it trust the kit: `grid key new kit`, then set the node's `[hub]` to `ip` = the kit's machine and `trust` = `kit.pub`.
3. Run:

```sh
grid test node --addr 10.0.0.2 --key node.pub --private kit.key --limit 2 --share --listen 10.0.0.1:9430
```

The kit opens jobs, pushes, shares, runs the kit cmds, tails, kills, pulls, and drops, and checks every answer; `--share` runs the share checks, for a node with its share on. `--listen` also checks `beat` and `post`: the node needs `signal = true`, and its `[hub]` `ip` and `port` set to that address (here `port = 9430`).

### A Hub

```sh
grid test hub --listen 0.0.0.0:9420 --private kit.key --hub-ip 10.0.0.1 --hub-key hub.pub --for 2m
```

Add the kit to the hub's config as a `[[nodes]]` entry (its machine's `ip`, `trust = "kit.pub"`, `tags = ["kit"]`), submit jobs with `tag = "kit"` that run the kit cmds, then read the report. When `--for` ends, it lists each call the hub made, any frame that broke the spec, and whether the hub dropped its jobs.

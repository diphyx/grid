# grid spec, version 1

The exact protocol. The docs explain it ([`docs/protocol.md`](../docs/protocol.md)); this file fixes it. Every hub and node must match it.
Golden bytes: [`vectors.json`](vectors.json). The test kit (`grid test`) checks the rest.

## 1. Connection

```
TCP → IP check → Noise handshake → frames → envelopes
```

1. The dialer opens TCP to `ip:port`; the default port is `9420`.
2. The listener closes the connection at once if the IP is not in its config.
3. Noise handshake, the dialer is the initiator:
   - pattern `Noise_IK_25519_ChaChaPoly_SHA256`
   - prologue: the ASCII bytes `grid/1`; a mismatch fails the handshake
   - the listener checks the dialer's static key against the key for that IP; no match → close
   - timeout 10 s → close
4. After the handshake, every frame is encrypted.
5. **Only the dialer calls.** The hub dials for job calls; the node dials its own connection for `beat` and `post`.

## 2. Keys

| Item                   | Format                                                                                                           |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------- |
| a key                  | X25519 (Curve25519), 32 bytes, standard base64 with padding: 44 chars                                            |
| `identity` in a config | who I am: a path only; the file holds the private key, base64, one line                                          |
| `trust` in a config    | whom I accept: the public key itself, or a path to a file holding it on one line; what decodes as a key is a key |

`grid key new <name>` writes such a pair: `<name>.key` (`0600`) and `<name>.pub`.

## 3. Frame

```
| length: u16, big-endian | payload: length bytes |
```

- The handshake messages use the same framing.
- After the handshake, the payload is one Noise ciphertext: max 65535 bytes; plaintext max 65519.
- One plaintext is one envelope. An envelope never spans frames.

## 4. Envelope

A CBOR map with text keys. Exactly one kind key; `id` on all but `ping` and `pong`.

| Envelope                           | From   | Is                                          |
| ---------------------------------- | ------ | ------------------------------------------- |
| `{id, call: str, body: map}`       | dialer | a call                                      |
| `{id, ok: map}`                    | either | success reply                               |
| `{id, reject: {reason, message?}}` | either | failure reply; also ends a stream           |
| `{id, data: any}`                  | either | a stream item                               |
| `{id, end: map}`                   | either | a stream is done                            |
| `{id, cancel: {}}`                 | dialer | stop a stream; the other side answers `end` |
| `{ping: {}}` / `{pong: {}}`        | either | keepalive                                   |

- `id`: uint ≥ 1, chosen by the dialer, unique among its open calls on that connection.
- Calls may overlap; replies may come in any order.
- A call gets exactly one `ok` or `reject`; a stream then ends with exactly one `end` or `reject`.
- After 15 s with no frame, a side sends `ping`; the other answers `pong`. No frame for 30 s → the connection is dead. A dead connection never touches a job.

## 5. Types

| Type       | CBOR                                                                  |
| ---------- | --------------------------------------------------------------------- |
| `str`      | text string, UTF-8                                                    |
| `uint`     | unsigned int, up to 64 bits                                           |
| `bytes`    | byte string                                                           |
| `[T]`      | array of T                                                            |
| `{str: T}` | map, text keys                                                        |
| `ms`       | `uint`, unix ms on the node's clock                                   |
| `dur`      | `uint`, ms; `0` = never                                               |
| `id`       | `str`, 32 lowercase hex chars (16 bytes)                              |
| `job`      | an `id`, random, chosen by the node                                   |
| `path`     | `str`, relative to the space, `/` separators; no `..`, no leading `/` |
| `?`        | optional; left out = none, or from the start                          |

- Unknown keys are ignored, so a new field never breaks an older peer.
- A missing required key or a wrong type → `reject invalid`.

## 6. Calls

Hub → node:

| Call   | `body`                                                 | `ok`                                                | Stream                                            |
| ------ | ------------------------------------------------------ | --------------------------------------------------- | ------------------------------------------------- |
| `open` | `{tag: str}`                                           | `{job: job, expires: dur}`                          | —                                                 |
| `push` | `{job: job, path?: path, id?: id}`                     | `{sha256: bytes, share: bool}`                      | see below                                         |
| `exec` | `{job: job, cmd: str, args?: [str], env?: {str: str}}` | `{}`                                                | —                                                 |
| `tail` | `{job: job, after?: uint}`                             | `{}`                                                | node → hub: `data` event…                         |
| `stat` | `{job: job}`                                           | `{state: str, cmd?: str, message?: str}`            | —                                                 |
| `kill` | `{job: job}`                                           | `{}`                                                | —                                                 |
| `pull` | `{job: job, path: path, since?: ms, offset?: uint}`    | `{files: [{path: path, size: uint, modified: ms}]}` | node → hub: `data` bytes…, `end {sha256?: bytes}` |
| `drop` | `{job: job}`                                           | `{}`                                                | —                                                 |

Node → hub, only with `signal = true`:

| Call   | `body`                                   | `ok` |
| ------ | ---------------------------------------- | ---- |
| `beat` | `{tags: [str], free: uint, share: bool}` | `{}` |
| `post` | `{job: job, event: event}`               | `{}` |

Rules per call:

| Call   | Rule                                                                                                                                                                                                                                                                                                                                                                          |
| ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `open` | no room for a job → `full`; tag not served → `unknown`                                                                                                                                                                                                                                                                                                                        |
| `push` | the receiver owns the offset: node sends `data {offset: uint, have: bool}` (bytes it already has for this `path`, `0` if new); hub sends `data` bytes from there, then `end {}`; node replies `ok {sha256, share}`, the sha256 of the whole stream from byte 0. With `id`, a share push (§10): `have: true` → hub sends no `data`, only `end {}`; `data` after it → `invalid` |
| `exec` | an `exec` on → `running`; no such cmd → `unknown`; `env` may not set `GRID_*` → `invalid`                                                                                                                                                                                                                                                                                     |
| `tail` | from the first event after `after` (`0` or left out = from the start); ends with `end {}` only on `drop` or `cancel`                                                                                                                                                                                                                                                          |
| `stat` | `cmd` and `message` left out while `idle`                                                                                                                                                                                                                                                                                                                                     |
| `kill` | no `exec` on → `ok`, nothing done                                                                                                                                                                                                                                                                                                                                             |
| `pull` | `offset` = bytes the hub already has; no files → `files: []`, no `data`, `end {}`                                                                                                                                                                                                                                                                                             |
| `drop` | kills a running `exec` first                                                                                                                                                                                                                                                                                                                                                  |
| all    | no such job → `unknown`; every job call resets `expires`                                                                                                                                                                                                                                                                                                                      |
| `beat` | every 5 s; the hub decides when a node is lost; `free` = jobs it can still open; `share` = its share is on                                                                                                                                                                                                                                                                    |
| `post` | resent in `seq` order until `ok`                                                                                                                                                                                                                                                                                                                                              |

## 7. States

| State     | When                                       |
| --------- | ------------------------------------------ |
| `idle`    | after `open`, before the first `exec`      |
| `running` | an `exec` is on                            |
| `done`    | the cmd exited `0`                         |
| `failed`  | the cmd exited not `0`, or could not start |
| `stopped` | `kill`, `timeout`, or `drop`               |

## 8. Events

`event = {seq: uint, time: ms, type: str, data: any}`

- `seq` starts at `1` per job and grows by `1`; no gaps.
- An event must fit in one frame.

| Type         | `data`                                                       |
| ------------ | ------------------------------------------------------------ |
| `log`        | `{stream: "out" \| "err", line: str}`                        |
| `state`      | `{state: str, cmd?: str, message?: str}`, the same as `stat` |
| `evt.<name>` | `{str: str}` or null: a cmd's own event, from `$GRID_EVENTS` |

- `log`: one event per line, without the newline; a line over 8 KiB is split.
- Own events: the cmd writes event lines to the file at `$GRID_EVENTS`: `[name]`, then optional space-separated `key=value` pairs (a value may be `"quoted"`, with `\"` and `\\`). `name` and keys match `[a-z0-9._-]{1,64}`; a line is at most 8 KiB. The node sends it as an event of type `evt.<name>` with the pairs as a map of text, or no data; nothing else a cmd writes can produce another type. A line that does not parse becomes a `log` `err` line.

## 9. Cmds

The node config defines them: `cmds: {name: {exec: [str], timeout?: dur}}`.

| The cmd gets   |                                                                  |
| -------------- | ---------------------------------------------------------------- |
| argv           | `exec` + `args`                                                  |
| working dir    | the space                                                        |
| env            | the node's env + `env` + `GRID_JOB`, `GRID_SPACE`, `GRID_EVENTS` |
| stdout, stderr | `log` events                                                     |

- `failed` → `message` = the last stderr line.
- `kill`, `timeout`, `drop` → SIGTERM, then SIGKILL after 10 s.
- The exec ends when its process exits; then whatever is left in its process group is killed.

## 10. Files

- Both ways, the stream is a tar, also for one file.
- `push`: entries are relative to `path` (default `.`), extracted when the stream ends.
- `pull`: entries are relative to the space; `path` picks a file or a folder; `since` keeps only files modified after it.

Tar rules:

| Item                      | Rule                                            |
| ------------------------- | ----------------------------------------------- |
| format                    | PAX                                             |
| order                     | entries sorted by path, bytewise                |
| owner                     | uid, gid `0`; no user or group names            |
| mode                      | `0644`, or `0755` if executable; folders `0755` |
| mtime                     | the file's, in seconds                          |
| symlinks                  | relative and inside the space; else `invalid`   |
| hardlinks, devices, fifos | `invalid`                                       |
| paths                     | no `..`, no leading `/`; else `invalid`         |

- The same files give the same bytes, so `offset` holds; if they changed, `sha256` differs and the hub pulls again from `0`.
- A stream chunk (`data` bytes) is at most 60 KiB.

Share pushes: a node may keep a **share**, push content kept for the jobs after, read only.

| Item     | Rule                                                                                                                      |
| -------- | ------------------------------------------------------------------------------------------------------------------------- |
| id       | the push tar's sha256, first 16 bytes; same id ⇒ same content                                                             |
| path     | required, not `.`; else `invalid`. The share is linked there, so it must be free; else `invalid`                          |
| hit      | the node has the id: `have: true`, no bytes; it links the share at `path`; `ok {sha256, share: true}`, `sha256` the tar's |
| miss     | the hub sends the tar; the node checks the id against its sha256 (else `invalid`), keeps it, links it; `ok {share: true}` |
| off      | share off, no room, or no link: a normal push into the space; `ok {share: false}`                                         |
| link     | a symlink (a junction on Windows) at `path` to the share, outside the space; pulling it is `invalid`, by the symlink rule |
| contents | read only: files `0444`, or `0555` if executable; folders `0555`. A cmd must not write there                              |
| keep     | a share a job links is kept while the job lives; others go after the node's `[share] expires` or past its `limit`         |

## 11. Rejects

| Reason    | When                     |
| --------- | ------------------------ |
| `full`    | no room for another job  |
| `running` | an `exec` is on          |
| `unknown` | no such tag, job, or cmd |
| `invalid` | bad input                |

`message` is for people; code reads only `reason`.

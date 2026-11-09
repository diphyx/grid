# Versions

> Each major is a generation: a fresh product, not an upgrade

A release is `MAJOR.MINOR.PATCH`, like `1.1.0`. The major is the **generation**: grid 1.

| Release                 | For                                             | Install                                      |
| ----------------------- | ----------------------------------------------- | -------------------------------------------- |
| patch `1.1.0` → `1.1.1` | a fix                                           | drop-in: same config, same state, same peers |
| minor `1.1` → `1.2`     | an addition: a new optional key, flag, or check | drop-in: same config, same state, same peers |
| major `1` → `2`         | anything that breaks the config, state, or wire | fresh: a new config, empty state             |

## A Generation

A generation holds to itself in three places, all set by its major:

| Where  | grid 1                       | Another generation's                                     |
| ------ | ---------------------------- | -------------------------------------------------------- |
| config | its first line is `grid = 1` | refused at start: `config is for grid 2, this is grid 1` |
| state  | under `/var/lib/grid/1/`     | never read: its folder stays as it was                   |
| peers  | the handshake says `grid/1`  | refused: a grid 1 hub and a grid 2 node never connect    |

Nothing of one generation is moved, converted, or read by the next. A `path` set in a config is used as it is: keep the generation in it, like `/data/grid/1/jobs`.

## Generations

| Generation | First release | Protocol | What it brought   |
| ---------- | ------------- | -------- | ----------------- |
| grid 1     | `1.0.0`       | `grid/1` | the first release |
| grid 2     | `2.0.0`       | `grid/2` | generations       |

## Moving to a New Generation

1. Drain: let the jobs finish, and pull what you need.
2. Install the new release on the hub and every node; they only work with each other.
3. Write fresh configs, `grid = 2` first, from that generation's docs. The keys may differ.
4. Start them: the state starts empty, in `/var/lib/grid/2/`.
5. Delete `/var/lib/grid/1/` when you no longer need it.

Docker images have a tag per generation: `ghcr.io/diphyx/grid-node:1` gets every grid 1 release and never grid 2; `latest` moves to a new generation when it comes out. Homebrew installs the latest release, of any generation.

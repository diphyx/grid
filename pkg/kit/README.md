# kit

The grid test kit: `grid test node`, `grid test hub`, `grid test vectors`. How to use it: [docs/protocol.md](../../docs/protocol.md#test-kit).

| Here                    | Is                                                                            |
| ----------------------- | ----------------------------------------------------------------------------- |
| `cmds/`                 | the kit cmds a node under test needs, and `node.toml` to append to its config |
| `node*.go`, `tailer.go` | the fake hub: every check against a node                                      |
| `fakenode*.go`          | the fake node: runs the kit cmds in memory, checks a hub's frames             |
| `vectors.go`            | the golden bytes in `spec/vectors.json`                                       |

`go test ./pkg/kit/` runs every node check against the kit's own fake node and checks the vectors are current.

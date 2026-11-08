// Package wire is the grid protocol in Go, shared by the kit, the node, and the hub.
// See spec/SPEC.md; section numbers in comments point there.
//
//	key.go       §2 keys
//	frame.go     §3 frames
//	conn.go      §1 connection, Noise IK, keepalive
//	envelope.go  §4 envelopes, CBOR
//	session.go   §4 routing by id: Start/Do for the dialer, Run(Handler) for the listener
//	types.go     §5–§8, §11 bodies, states, events, rejects, checks
//	tar.go       §10 tar streams in memory
//	fs.go        §10 tar streams to and from a folder (os.Root)
//	eventlog.go  a job's events on disk, numbered by seq
//	duration.go  config durations: 10m, 30d, 0
//	size.go      config sizes: 512MB, 10GB, 0
package wire

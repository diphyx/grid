// Package node is the reference grid node: it runs the hub's jobs in spaces on disk.
// Embed it (New, Listen, Serve), or run it as a daemon with `grid node run`.
//
//	config.go   node.toml
//	node.go     New, Listen, Serve, Close; restart; expiry; history (pkg/history)
//	calls.go    the calls (§6): open, exec, tail, stat, kill, push, pull, drop
//	job.go      a job on disk: meta, space, events, posts
//	run.go      a cmd's process: args, env, logs, own events, stop (§9)
//	files.go    push and pull (§10)
//	share.go    the share: share pushes kept for the jobs after, linked into their spaces
//	link_*.go   the link into a space: a symlink, a junction on Windows
//	signal.go   beat and post, with signal on
//	local.go    the socket API
package node

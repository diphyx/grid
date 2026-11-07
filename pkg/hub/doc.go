// Package hub is the reference grid hub: it finds nodes, sends them jobs, and keeps the results.
// Embed it (New, Start, Submit), or run it as a daemon with `grid hub run`.
//
//	config.go    hub.toml
//	job.go       a job file: tag, push, steps, pull, retries
//	hub.go       New, Start, Close, Submit, Kill, Drop, Wait, Events, Files, Pack
//	nodes.go     one link per node: session, beat, lost, picking a node, calls with retry
//	run.go       the worker: place, steps, failover, resume
//	follow.go    the node job's tail, passed on into the hub job's log
//	transfer.go  push and pull, resumable
//	store.go     a job on disk: record, events, push tars, pulled files
//	signal.go    beat and post, with signal on
//	local.go     the socket API
package hub

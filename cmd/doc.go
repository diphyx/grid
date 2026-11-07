// Package cmd is the `grid` command: the hub and node daemons, their CLI, and the test kit.
//
//	cli.go     Run, usage, flags
//	client.go  the socket client and the tail printer, for both sides
//	node.go    grid node run | status | jobs | job | tail
//	hub.go     grid hub run | submit | status | nodes | jobs | job | tail | pull | kill | drop
//	test.go    grid test node | hub | vectors
//	key.go     grid key new | pub
//	format.go  times and blanks for the views
package cmd

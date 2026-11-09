package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/diphyx/grid/v2/pkg/hub"
	"github.com/diphyx/grid/v2/pkg/node"
)

const USAGE = `grid: the hub and node daemons, their CLI, keys, and the test kit

Node:
  grid node run [--config /etc/grid/node.toml]   the node daemon
  grid node status                               tags, jobs, share, uptime, hub connected
  grid node jobs                                 open jobs
  grid node job <id>                             one job: cmds, files, state, times
  grid node tail <id> [--after N]                follow a job's events
  grid node history [--kind K]                   the event log: job, share, node, hub

Hub:
  grid hub run [--config /etc/grid/hub.toml]     the hub daemon
  grid hub submit <job.toml> [--wait] [--repack] send a job; prints its id
  grid hub status                                key, signal, nodes online, jobs by state
  grid hub nodes                                 every node: tags, online or lost, share, free
  grid hub jobs                                  every job it keeps
  grid hub job <id>                              one job: node, steps, state, files
  grid hub tail <id> [--after N]                 follow a job's events
  grid hub pull <id> <dir>                       copy a job's pulled files out
  grid hub kill <id>                             stop a running job
  grid hub drop <id>                             forget a job and its files
  grid hub history [--kind K]                    the event log: job, node, hub

Keys:
  grid key new <name>                            write <name>.key (0600) and <name>.pub
  grid key pub <name>.key                        print the public key

Test kit:
  grid test node | hub | vectors                 grid test for the flags

Version:
  grid version                                   this binary's version and protocol

Flags: --json for scripts, --watch for a live view, --socket PATH
(default /run/grid/node.sock, /run/grid/hub.sock).
`

// Run runs `grid` with its arguments, without the program name, and returns the exit code.
func Run(args []string) int {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		return printVersion()
	}
	if len(args) > 0 && args[0] == "test" {
		return test(args[1:])
	}
	if len(args) > 0 && args[0] == "key" {
		return key(args[1:])
	}
	if len(args) >= 2 && args[0] == "hub" {
		return hubMain(args[1], parse(args[2:], hub.DEFAULT_CONFIG, hub.DEFAULT_SOCKET))
	}
	if len(args) < 2 || args[0] != "node" {
		fmt.Fprint(os.Stderr, USAGE)

		return 2
	}
	command, rest := args[1], args[2:]
	options := parse(rest, node.DEFAULT_CONFIG, node.DEFAULT_SOCKET)
	switch command {
	case "run":
		{
			runNode(options)
		}
	case "status", "jobs", "job", "tail", "history":
		{
			view(command, options)
		}
	default:
		{
			fmt.Fprint(os.Stderr, USAGE)

			return 2
		}
	}

	return 0
}

type flags struct {
	pos    []string
	config string
	socket string
	json   bool
	watch  bool
	wait   bool
	repack bool
	after  string
	kind   string
}

// parse reads flags anywhere among the args.
func parse(args []string, config, socket string) flags {
	options := flags{config: config, socket: socket}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		val := func() string {
			key, value, ok := strings.Cut(arg, "=")
			if ok && strings.HasPrefix(key, "-") {
				return value
			}
			if i+1 >= len(args) {
				fail(fmt.Errorf("%s: needs a value", arg))
			}
			i++

			return args[i]
		}
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") {
			options.pos = append(options.pos, arg)
			continue
		}
		switch name {
		case "config":
			{
				options.config = val()
			}
		case "socket":
			{
				options.socket = val()
			}
		case "after":
			{
				options.after = val()
			}
		case "json":
			{
				options.json = true
			}
		case "watch":
			{
				options.watch = true
			}
		case "kind":
			{
				options.kind = val()
			}
		case "repack":
			{
				options.repack = true
			}
		case "wait":
			{
				options.wait = true
			}
		default:
			{
				fail(fmt.Errorf("unknown flag %s", arg))
			}
		}
	}

	return options
}

func fail(problem error) {
	fmt.Fprintln(os.Stderr, "grid:", problem)
	os.Exit(1)
}

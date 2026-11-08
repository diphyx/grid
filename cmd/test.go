package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/diphyx/grid/pkg/kit"
	"github.com/diphyx/grid/pkg/wire"
)

// test is `grid test`: the test kit.
func test(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, TEST_USAGE)

		return 2
	}
	switch args[0] {
	case "node":
		{
			return testNode(args[1:])
		}
	case "hub":
		{
			return testHub(args[1:])
		}
	case "vectors":
		{
			vectors, makeVectorsError := kit.MakeVectors()
			if makeVectorsError != nil {
				fail(makeVectorsError)
			}
			content, _ := json.MarshalIndent(vectors, "", "  ")
			fmt.Println(string(content))

			return 0
		}
	default:
		{
			fmt.Fprint(os.Stderr, TEST_USAGE)

			return 2
		}
	}
}

const TEST_USAGE = `grid test: the grid test kit

  grid test node    --addr IP[:PORT] --key NODE_PUBLIC --private HUB_PRIVATE_FILE
                    [--tag kit] [--limit N] [--share] [--listen IP:PORT] [--json]
      Acts as the hub and runs every check against a node.
      The node must trust the kit's key, serve --tag, and have the cmds in pkg/kit/cmds/node.toml.
      --limit is its [jobs] limit; --share checks share pushes, for a node with [share] on.
      --listen also checks beat and post: the node needs signal = true and [hub] at that address.

  grid test hub     --listen IP[:PORT] --private NODE_PRIVATE_FILE --hub-ip IP --hub-key HUB_PUBLIC
                    [--tag kit] [--limit 2] [--share] [--signal IP:PORT] [--for 60s] [--json]
      Acts as a node with the kit cmds. Point the hub at it and run jobs;
      every frame the hub sends is checked. Stops after --for or Ctrl-C.
      --share keeps share pushes, in memory.
      --signal sends beat and post to the hub at that address.

  grid test vectors
      Prints the golden vectors: keys, handshake, frames, envelopes.

Keys come from grid key new. The exit code is the number of failed checks.
`

func testAddr(text string) string {
	_, _, splitHostPortError := net.SplitHostPort(text)
	if splitHostPortError == nil {
		return text
	}
	return wire.Addr(text, 0)
}

func testExit(count int) int { return min(count, 125) }

func testNode(args []string) int {
	flagSet := flag.NewFlagSet("node", flag.ExitOnError)
	target := flagSet.String("addr", "", "node IP[:PORT]")
	key := flagSet.String("key", "", "node public key, or a path to it")
	private := flagSet.String("private", "", "the kit's private key file (the node trusts its public key)")
	tag := flagSet.String("tag", "kit", "tag the node serves for the kit")
	limit := flagSet.Int("limit", 0, "the node's [jobs] limit; 0 = skip the full check")
	share := flagSet.Bool("share", false, "the node's share is on: check share pushes")
	listen := flagSet.String("listen", "", "IP:PORT to accept beat and post")
	asJSON := flagSet.Bool("json", false, "JSON output")
	_ = flagSet.Parse(args)
	if *target == "" || *key == "" || *private == "" {
		fail(errors.New("node: --addr, --key, --private are required"))
	}
	nodeKey, loadKeyError := wire.LoadKey(*key)
	if loadKeyError != nil {
		fail(loadKeyError)
	}
	hubKey, loadKeyError := wire.LoadPrivate(*private)
	if loadKeyError != nil {
		fail(loadKeyError)
	}
	report := kit.NewReport(os.Stdout, *asJSON)
	kit.TestNode(kit.NodeTarget{Addr: testAddr(*target), NodeKey: nodeKey, Hub: hubKey, Tag: *tag, Limit: *limit, Share: *share, Listen: *listen}, report)

	return testExit(report.Finish())
}

func testHub(args []string) int {
	flagSet := flag.NewFlagSet("hub", flag.ExitOnError)
	listen := flagSet.String("listen", "", "IP[:PORT] to listen on")
	private := flagSet.String("private", "", "the kit's private key file, as the node")
	hubIP := flagSet.String("hub-ip", "", "the hub's IP")
	hubKey := flagSet.String("hub-key", "", "the hub's public key, or a path to it")
	tag := flagSet.String("tag", "kit", "tag to serve")
	limit := flagSet.Int("limit", 2, "jobs at once")
	share := flagSet.Bool("share", false, "keep share pushes")
	collector := flagSet.String("signal", "", "hub IP:PORT to send beat and post to")
	dur := flagSet.Duration("for", 60*time.Second, "how long to run")
	asJSON := flagSet.Bool("json", false, "JSON output")
	_ = flagSet.Parse(args)
	if *listen == "" || *private == "" || *hubIP == "" || *hubKey == "" {
		fail(errors.New("hub: --listen, --private, --hub-ip, --hub-key are required"))
	}
	nodeKey, loadPrivateError := wire.LoadPrivate(*private)
	if loadPrivateError != nil {
		fail(loadPrivateError)
	}
	hubPublic, loadPrivateError := wire.LoadKey(*hubKey)
	if loadPrivateError != nil {
		fail(loadPrivateError)
	}
	listener, loadPrivateError := net.Listen("tcp", testAddr(*listen))
	if loadPrivateError != nil {
		fail(loadPrivateError)
	}
	report := kit.NewReport(os.Stdout, *asJSON)
	fakeNode := &kit.FakeNode{Key: nodeKey, Peers: wire.Peers{*hubIP: hubPublic}, Tags: []string{*tag}, Limit: *limit, Share: *share, Report: report}
	if *collector != "" {
		fakeNode.Hub, fakeNode.HubKey = testAddr(*collector), hubPublic
	}
	go func() { _ = fakeNode.Serve(listener) }()
	if !*asJSON {
		fmt.Printf("fake node on %s, public key %s; running %s\n\n", listener.Addr(), nodeKey.Public, *dur)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	select {
	case <-stop:
	case <-time.After(*dur):
	}
	fakeNode.Close()
	fakeNode.Summary(report)

	return testExit(report.Finish())
}

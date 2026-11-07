package kit

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// TestFakeNode runs every node check against the fake node: the kit tests itself.
func TestFakeNode(t *testing.T) {
	hub, _ := wire.GenerateKeypair(nil)
	node, _ := wire.GenerateKeypair(nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sigAddr := sig.Addr().String()
	sig.Close()
	f := &FakeNode{Key: node, Peers: wire.Peers{"127.0.0.1": hub.Public}, Slots: 2, Hub: sigAddr, HubKey: hub.Public}
	go f.Serve(ln)
	defer f.Close()

	out := io.Discard
	if testing.Verbose() {
		out = os.Stdout
	}
	r := NewReport(out, false)
	TestNode(NodeTarget{Addr: ln.Addr().String(), NodeKey: node.Public, Hub: hub, Slots: 2, Listen: sigAddr, Wait: 10 * time.Second}, r)
	for _, x := range r.All {
		if !x.Pass {
			t.Errorf("%s %s: %s", x.Spec, x.Name, x.Note)
		}
	}
	if len(r.All) < 40 {
		t.Errorf("only %d checks ran", len(r.All))
	}
}

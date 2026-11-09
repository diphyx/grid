package kit

import (
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// TestFakeNode runs every node check against the fake node: the kit tests itself.
func TestFakeNode(t *testing.T) {
	hub, _ := wire.GenerateKeypair(nil)
	node, _ := wire.GenerateKeypair(nil)
	ln, err := net.Listen("tcp", ":0") // 127.0.0.1 for the hub, ::1 for the second hub
	if err != nil {
		t.Fatal(err)
	}
	sig, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sigAddr := sig.Addr().String()
	sig.Close()
	other, _ := wire.GenerateKeypair(nil)
	f := &FakeNode{Key: node, Peers: wire.Peers{"127.0.0.1": hub.Public, "::1": other.Public}, Limit: 2, Share: true, Hub: sigAddr, HubKey: hub.Public}
	go f.Serve(ln)
	defer f.Close()

	out := io.Discard
	if testing.Verbose() {
		out = os.Stdout
	}
	r := NewReport(out, false)
	addr := fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	TestNode(NodeTarget{Addr: addr, Other: other, OtherAddr: fmt.Sprintf("[::1]:%d", ln.Addr().(*net.TCPAddr).Port), NodeKey: node.Public, Hub: hub, Limit: 2, Share: true, Listen: sigAddr, Wait: 10 * time.Second}, r)
	for _, x := range r.All {
		if !x.Pass {
			t.Errorf("%s %s: %s", x.Spec, x.Name, x.Note)
		}
	}
	if len(r.All) < 40 {
		t.Errorf("only %d checks ran", len(r.All))
	}
}

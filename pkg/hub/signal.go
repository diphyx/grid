package hub

import (
	"net"

	"github.com/diphyx/grid/pkg/wire"
)

// listen accepts the nodes' beat and post, with signal on (§6). Every node in the config may dial.
func (hub *Hub) listen() error {
	listener, listenError := net.Listen("tcp", wire.Addr("", hub.config.Port))
	if listenError != nil {
		return listenError
	}
	hub.listener = listener
	peers := wire.Peers{}
	for _, each := range hub.links {
		peers[each.peer.IP] = each.key
	}
	go func() {
		for {
			connection, acceptError := listener.Accept()
			if acceptError != nil {
				return
			}
			go func() {
				secure, serverError := wire.NewServer(connection, hub.key, peers, nil)
				if serverError != nil {
					_ = connection.Close()

					return
				}
				from := hub.linkByIP(wire.RemoteIP(connection))
				_ = wire.NewSession(secure).Run(func(request *wire.Request) { hub.signal(from, request) })
			}()
		}
	}()

	return nil
}

func (hub *Hub) linkByIP(ip string) *link {
	for _, each := range hub.links {
		if wire.NormIP(each.peer.IP) == ip {
			return each
		}
	}

	return nil
}

// signal answers one beat or post; any other call is invalid here.
func (hub *Hub) signal(from *link, request *wire.Request) {
	switch request.Call {
	case wire.CALL_BEAT:
		{
			var beat wire.Beat
			decodeError := wire.DecodeBody(request.Body, &beat)
			if decodeError != nil {
				_ = request.Fail(decodeError)

				return
			}
			if from != nil {
				from.beat(beat)
			}
		}
	case wire.CALL_POST:
		{
			var post wire.Post
			decodeError := wire.DecodeBody(request.Body, &post)
			if decodeError != nil {
				_ = request.Fail(decodeError)

				return
			}
			// The news only: the job's tail already carries the same event; a post says the node is up.
			if from != nil {
				from.seen()
			}
		}
	default:
		{
			_ = request.Fail(wire.NewReject(wire.INVALID, "call %q is not for a hub", request.Call))

			return
		}
	}
	_ = request.OK(wire.Empty{})
}

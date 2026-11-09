package node

import (
	"sync/atomic"
	"time"

	"github.com/diphyx/grid/v2/pkg/history"
	"github.com/diphyx/grid/v2/pkg/wire"
)

// hubLink is one trusted hub: its key, where it listens, and its signal connection. A job
// is its opener's: Meta.Hub is the link's ip, and any other hub gets unknown for it.
type hubLink struct {
	ip     string // normalized, as Meta.Hub and history entries hold it
	addr   string // ip:port, for signal
	key    wire.Key
	posts  chan struct{} // wakes its signal loop
	online atomic.Bool   // signal connection up
}

// newHubLinks makes a link per [[hubs]] entry; keys are in the same order.
func newHubLinks(peers []Peer, keys []wire.Key) []*hubLink {
	links := make([]*hubLink, len(peers))
	for index, peer := range peers {
		links[index] = &hubLink{ip: wire.NormIP(peer.IP), addr: wire.Addr(peer.IP, peer.Port), key: keys[index], posts: make(chan struct{}, 1)}
	}

	return links
}

// peers is what the handshake accepts: each hub's IP with its key.
func (node *Node) peers() wire.Peers {
	peers := wire.Peers{}
	for _, link := range node.hubs {
		peers[link.ip] = link.key
	}

	return peers
}

// hubAt is the trusted hub at ip; the handshake has already matched it.
func (node *Node) hubAt(ip string) *hubLink {
	for _, link := range node.hubs {
		if link.ip == ip {
			return link
		}
	}

	return nil
}

// dropUntrusted removes a job found on disk at start whose hub is no longer in the config.
func (node *Node) dropUntrusted(job *Job) {
	job.mutex.Lock()
	removeError := job.remove()
	job.mutex.Unlock()
	if removeError != nil {
		node.log.Error("drop: clean up", "job", job.ID(), "err", removeError)
	}
	node.history.Add(history.Entry{Kind: history.KIND_JOB, Event: "orphaned", Subject: job.ID(), Hub: job.meta.Hub, Message: "hub not trusted",
		Took: time.Since(job.meta.Opened)})
	node.log.Debug("orphaned", "job", job.ID(), "hub", job.meta.Hub)
}

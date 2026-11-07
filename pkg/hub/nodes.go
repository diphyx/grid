package hub

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// Liveness (docs/job.md, "When Things Fail"); vars so tests can shorten them.
var (
	beatLost = 15 * time.Second // a node that beat, then went silent this long, is lost
	callLost = 30 * time.Second // a node that never beat, with calls failing this long, is lost
)

// link is the hub's view of one node and its one session (calls overlap on it).
type link struct {
	peer    Peer
	address string
	key     wire.Key

	mutex        sync.Mutex
	dialing      sync.Mutex // one dial at a time, so calls share one session
	session      *wire.Session
	hasBeat      bool
	lastBeat     time.Time
	free         uint64
	lastSeen     time.Time
	failingSince time.Time
	running      int
}

// NodeView is `grid hub nodes`.
type NodeView struct {
	Addr     string    `json:"address"`
	Tags     []string  `json:"tags"`
	Online   bool      `json:"online"`
	Lost     bool      `json:"lost"`
	Signal   bool      `json:"signal"` // it sends beat
	Free     *uint64   `json:"free,omitempty"`
	Running  int       `json:"running"`
	LastSeen time.Time `json:"last_seen,omitzero"`
}

// connect returns the node's session, dialing if there is none or it ended.
func (link *link) connect(local wire.Keypair) (*wire.Session, error) {
	link.dialing.Lock()
	defer link.dialing.Unlock()

	link.mutex.Lock()
	session := link.session
	link.mutex.Unlock()
	if session != nil {
		select {
		case <-session.Done():
			{
				session = nil
			}
		default:
			{
				return session, nil
			}
		}
	}
	connection, connectError := wire.Connect(link.address, local, link.key, nil)
	if connectError != nil {
		link.failed()

		return nil, connectError
	}
	session = wire.NewSession(connection)
	go func() { _ = session.Run(nil) }()
	link.mutex.Lock()
	link.session = session
	link.mutex.Unlock()
	link.seen()

	return session, nil
}

// seen marks a call that worked.
func (link *link) seen() {
	link.mutex.Lock()
	defer link.mutex.Unlock()

	link.lastSeen = time.Now()
	link.failingSince = time.Time{}
}

// failed marks a call that did not reach the node.
func (link *link) failed() {
	link.mutex.Lock()
	defer link.mutex.Unlock()

	if link.failingSince.IsZero() {
		link.failingSince = time.Now()
	}
}

// beat records a beat from the node.
func (link *link) beat(free uint64) {
	link.mutex.Lock()
	defer link.mutex.Unlock()

	link.hasBeat, link.lastBeat, link.free = true, time.Now(), free
	link.lastSeen, link.failingSince = link.lastBeat, time.Time{}
}

// isLost applies the rule in docs/job.md, "When Things Fail".
func (link *link) isLost() bool {
	link.mutex.Lock()
	defer link.mutex.Unlock()

	if link.hasBeat {
		return time.Since(link.lastBeat) > beatLost
	}

	return !link.failingSince.IsZero() && time.Since(link.failingSince) > callLost
}

func (link *link) view() NodeView {
	lost := link.isLost()
	link.mutex.Lock()
	defer link.mutex.Unlock()

	view := NodeView{Addr: link.address, Tags: link.peer.Tags, Lost: lost, Signal: link.hasBeat, Running: link.running, LastSeen: link.lastSeen}
	view.Online = !lost && link.failingSince.IsZero() && !link.lastSeen.IsZero()
	if link.hasBeat {
		free := link.free
		view.Free = &free
	}

	return view
}

func (link *link) hasTag(tag string) bool {
	for _, name := range link.peer.Tags {
		if name == tag {
			return true
		}
	}

	return false
}

// candidates lists the nodes to try for a tag, skipped ones aside: the most free first, then
// the fewest jobs from this hub, ties round robin by turn; lost ones last, since only a call
// finds a node without beats again.
func (hub *Hub) candidates(tag string, skip map[string]bool) []*link {
	hub.mutex.Lock()
	turn := hub.turn
	hub.turn++
	hub.mutex.Unlock()
	type scored struct {
		link    *link
		lost    bool
		free    int64
		running int
		order   int
	}
	var list []scored
	for i, candidate := range hub.links {
		if !candidate.hasTag(tag) || skip[candidate.address] {
			continue
		}
		lost := candidate.isLost()
		candidate.mutex.Lock()
		free := int64(-1) // unknown: after the nodes known to have room
		if candidate.hasBeat {
			free = int64(candidate.free)
		}
		running := candidate.running
		candidate.mutex.Unlock()
		list = append(list, scored{candidate, lost, free, running, (i + len(hub.links) - turn%max(len(hub.links), 1)) % max(len(hub.links), 1)})
	}
	sort.SliceStable(list, func(left, right int) bool {
		if list[left].lost != list[right].lost {
			return !list[left].lost
		}
		if list[left].free != list[right].free {
			return list[left].free > list[right].free
		}
		if list[left].running != list[right].running {
			return list[left].running < list[right].running
		}

		return list[left].order < list[right].order
	})
	out := make([]*link, len(list))
	for i, item := range list {
		out[i] = item.link
	}

	return out
}

func (hub *Hub) linkOf(address string) *link {
	for _, each := range hub.links {
		if each.address == address {
			return each
		}
	}

	return nil
}

// call runs fn on the node's session, again while the node cannot be reached, until it is lost.
// A reject is an answer: it is returned, not retried. A closed stop ends the retries with errHalt;
// nil keeps trying.
func (hub *Hub) call(stop <-chan struct{}, on *link, callback func(session *wire.Session) error) error {
	for {
		session, connectError := on.connect(hub.key)
		if connectError == nil {
			callError := callback(session)
			if callError == nil {
				on.seen()

				return nil
			}
			var reject *wire.Reject
			if errors.As(callError, &reject) {
				on.seen()

				return reject
			}
			if errors.Is(callError, errHalt) || errors.Is(callError, errGone) {
				return callError
			}
			on.failed()
		}
		if on.isLost() {
			return errLost
		}
		select {
		case <-hub.stop:
			{
				return errClosing
			}
		case <-stop:
			{
				return errHalt
			}
		case <-time.After(CALL_RETRY):
			{
			}
		}
	}
}

package node

import (
	"time"

	"github.com/diphyx/grid/pkg/history"
	"github.com/diphyx/grid/pkg/wire"
)

// onState wakes the signal loop: there is a state event to post (§6).
func (node *Node) onState() {
	if !node.config.Signal {
		return
	}
	select {
	case node.posts <- struct{}{}:
		{
		}
	default:
		{
		}
	}
}

func (node *Node) free() uint64 {
	node.mutex.Lock()
	defer node.mutex.Unlock()

	return uint64(max(node.config.Jobs.Limit-len(node.jobs), 0))
}

// signal dials the hub, sends beat every 5 s, and posts every state event in order until it is
// acked. What was acked is saved per job, so after a restart the rest is sent again.
func (node *Node) signal() {
	address := wire.Addr(node.config.Hub.IP, node.config.Hub.Port)
	var session *wire.Session
	ticker := time.NewTicker(wire.BEAT_EVERY)
	defer ticker.Stop()

	isBeatDue := true
	drop := func() {
		if session != nil {
			_ = session.Close()
			session = nil
			node.history.Add(history.Entry{Kind: history.KIND_HUB, Event: "disconnected", Subject: address})
		}
		node.hubOnline.Store(false)
	}
	for {
		if session != nil {
			select {
			case <-session.Done():
				{
					drop()
				}
			default:
				{
				}
			}
		}
		if session == nil {
			connection, connectError := wire.Connect(address, node.key, node.hubKey, nil)
			if connectError == nil {
				opened := wire.NewSession(connection)
				go func() {
					_ = opened.Run(nil)
					node.hubOnline.Store(false)
				}()
				session = opened
				node.hubOnline.Store(true)
				node.history.Add(history.Entry{Kind: history.KIND_HUB, Event: "connected", Subject: address})
			}
		}
		if session != nil && isBeatDue {
			beatError := session.Do(wire.CALL_BEAT, wire.Beat{Tags: node.config.Tags, Free: node.free(), Share: node.config.Share.Limit.Bytes > 0}, nil)
			if beatError != nil {
				drop()
			}
			isBeatDue = false
		}
		if session != nil {
			postError := node.postAll(session)
			if postError != nil {
				drop()
			}
		}
		select {
		case <-node.stop:
			{
				drop()

				return
			}
		case <-node.posts:
			{
			}
		case <-ticker.C:
			{
				isBeatDue = true
			}
		}
	}
}

// postAll sends every job's unacked state events, in seq order per job. A reject skips the
// event; a broken connection returns, and the next try starts from what was acked.
func (node *Node) postAll(session *wire.Session) error {
	node.mutex.Lock()
	jobs := make([]*Job, 0, len(node.jobs))
	for _, each := range node.jobs {
		jobs = append(jobs, each)
	}
	node.mutex.Unlock()
	for _, job := range jobs {
		states, readTo := job.unposted()
		for _, event := range states {
			postError := session.Do(wire.CALL_POST, wire.Post{Job: job.ID(), Event: event}, nil)
			if postError != nil {
				_, isReject := postError.(*wire.Reject)
				if !isReject {
					return postError
				}
				node.log.Error("post rejected", "job", job.ID(), "seq", event.Seq, "err", postError)
			}
			job.markPosted(event.Seq)
		}
		job.markPosted(readTo)
	}

	return nil
}

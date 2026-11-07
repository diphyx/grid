package kit

import (
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// signal sends beat every 5 s and posts state events, resent until ok (§6).
func (fakeNode *FakeNode) signal() {
	var session *wire.Session
	var pending []postItem
	ticker := time.NewTicker(wire.BEAT_EVERY)
	defer ticker.Stop()

	isBeatDue := true
	for {
		if session == nil {
			connection, connectError := wire.Connect(fakeNode.Hub, fakeNode.Key, fakeNode.HubKey, nil)
			if connectError == nil {
				session = wire.NewSession(connection)
				go func() { _ = session.Run(nil) }()
			}
		}
		if session != nil && isBeatDue {
			fakeNode.mutex.Lock()
			used := fakeNode.usedLocked()
			fakeNode.mutex.Unlock()
			doError := session.Do(wire.CALL_BEAT, wire.Beat{Tags: fakeNode.Tags, Free: uint64(fakeNode.Slots - used)}, nil)
			if doError != nil {
				session.Close()
				session = nil
			}
			isBeatDue = false
		}
		for session != nil && len(pending) > 0 {
			item := pending[0]
			doError := session.Do(wire.CALL_POST, wire.Post{Job: item.job, Event: item.event}, nil)
			if doError != nil {
				session.Close()
				session = nil
				break
			}
			pending = pending[1:]
		}
		select {
		case <-fakeNode.stop:
			if session != nil {
				session.Close()
			}
			return
		case item := <-fakeNode.posts:
			pending = append(pending, item)
		case <-ticker.C:
			isBeatDue = true
		}
	}
}

package wire

import (
	"errors"
	"sync"
)

var ErrClosed = errors.New("connection closed")

// Session runs one connection: it routes frames by id and answers ping.
// The dialer uses Call; the listener passes a Handler to Serve.
type Session struct {
	connection *Conn
	mutex      sync.Mutex
	next       uint64
	calls      map[uint64]*route
	problem    error
	done       chan struct{}
	OnPong     func()                                  // optional
	OnBad      func(envelope *Envelope, problem error) // optional: a frame that breaks §4
}

// NewSession wraps a connection; call Run to start reading.
func NewSession(connection *Conn) *Session {
	return &Session{connection: connection, calls: map[uint64]*route{}, done: make(chan struct{})}
}

// Conn is the session's connection.
func (session *Session) Conn() *Conn { return session.connection }

// Done is closed when the session ends.
func (session *Session) Done() <-chan struct{} { return session.done }

// Close ends the session.
func (session *Session) Close() error { return session.connection.Close() }

// Send writes one envelope.
func (session *Session) Send(envelope *Envelope) error { return session.connection.Send(envelope) }

// route is where one open id's frames go; gone closes when nobody reads them any more.
type route struct {
	in   chan *Envelope
	gone chan struct{}
}

func (session *Session) open(id uint64) *route {
	opened := &route{in: make(chan *Envelope, 64), gone: make(chan struct{})}
	session.mutex.Lock()
	session.calls[id] = opened
	session.mutex.Unlock()

	return opened
}

// route hands a frame to its call; it never waits on a call that was closed meanwhile.
func (session *Session) route(envelope *Envelope) bool {
	session.mutex.Lock()
	found, ok := session.calls[envelope.ID]
	session.mutex.Unlock()
	if ok {
		select {
		case found.in <- envelope:
		case <-found.gone:
		case <-session.done:
		}
	}

	return ok
}

func (session *Session) forget(id uint64) {
	session.mutex.Lock()
	found := session.calls[id]
	delete(session.calls, id)
	session.mutex.Unlock()
	if found != nil {
		close(found.gone)
	}
}

func (session *Session) finish(problem error) {
	session.mutex.Lock()
	session.problem = problem
	session.mutex.Unlock()
	close(session.done)
	session.connection.Close()
}

// Err is why the session ended.
func (session *Session) Err() error {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.problem
}

// Run reads until the connection ends. h handles incoming calls; nil = none expected.
func (session *Session) Run(handler Handler) error {
	go session.connection.Keepalive()
	for {
		envelope, recvError := session.connection.Recv()
		if recvError != nil {
			session.finish(recvError)

			return recvError
		}
		checkError := envelope.Check()
		if checkError != nil {
			if session.OnBad != nil {
				session.OnBad(envelope, checkError)
			}
			continue
		}
		switch envelope.Kind() {
		case KIND_PING:
			{
				_ = session.connection.Send(&Envelope{Pong: &Empty{}})
			}
		case KIND_PONG:
			{
				if session.OnPong != nil {
					session.OnPong()
				}
			}
		case KIND_CALL:
			{
				if handler == nil {
					if session.OnBad != nil {
						session.OnBad(envelope, errors.New("call from the listener"))
					}
					continue
				}
				session.mutex.Lock()
				_, dup := session.calls[envelope.ID]
				session.mutex.Unlock()
				if dup {
					if session.OnBad != nil {
						session.OnBad(envelope, errors.New("id already open"))
					}
					continue
				}
				opened := session.open(envelope.ID)
				request := &Request{ID: envelope.ID, Call: envelope.Call, Body: envelope.Body, In: opened.in, gone: opened.gone, session: session}
				go func() {
					defer session.forget(request.ID)

					handler(request)
				}()
			}
		default:
			{
				if !session.route(envelope) && session.OnBad != nil {
					session.OnBad(envelope, errors.New("no open call with this id"))
				}
			}
		}
	}
}

// Call is one outgoing call; read replies and stream frames from C.
type Call struct {
	ID uint64
	C  <-chan *Envelope

	gone    <-chan struct{}
	session *Session
}

// Start sends a call and returns it open.
func (session *Session) Start(call string, body any) (*Call, error) {
	session.mutex.Lock()
	session.next++
	id := session.next
	session.mutex.Unlock()
	opened := session.open(id)
	sendError := session.connection.Send(&Envelope{ID: id, Call: call, Body: MustRaw(body)})
	if sendError != nil {
		session.forget(id)

		return nil, sendError
	}

	return &Call{ID: id, C: opened.in, gone: opened.gone, session: session}, nil
}

// Next waits for the next frame of the call.
func (call *Call) Next() (*Envelope, error) {
	select {
	case envelope := <-call.C:
		return envelope, nil
	case <-call.gone:
		return nil, ErrClosed
	case <-call.session.done:
		select {
		case envelope := <-call.C:
			return envelope, nil
		default:
		}
		return nil, ErrClosed
	}
}

// Reply waits for ok or reject; ok is decoded into out (nil = ignore).
func (call *Call) Reply(out any) error {
	for {
		envelope, nextError := call.Next()
		if nextError != nil {
			return nextError
		}
		switch {
		case envelope.Reject != nil:
			{
				return envelope.Reject
			}
		case envelope.OK != nil:
			{
				if out == nil {
					return nil
				}
				return Unmarshal(envelope.OK, out)
			}
		}
		// data before the reply belongs to the caller; Reply is for plain calls.
	}
}

// Data sends a stream item for this call.
func (call *Call) Data(value any) error {
	return call.session.connection.Send(&Envelope{ID: call.ID, Data: MustRaw(value)})
}

// End ends this call's stream.
func (call *Call) End(value any) error {
	return call.session.connection.Send(&Envelope{ID: call.ID, End: MustRaw(value)})
}

// Cancel asks the other side to stop this call's stream.
func (call *Call) Cancel() error {
	return call.session.connection.Send(&Envelope{ID: call.ID, Cancel: &Empty{}})
}

// Close forgets the call; later frames for its id are dropped.
func (call *Call) Close() { call.session.forget(call.ID) }

// Do runs a plain call: send, wait for ok or reject.
func (session *Session) Do(call string, body, out any) error {
	started, startError := session.Start(call, body)
	if startError != nil {
		return startError
	}
	defer started.Close()

	return started.Reply(out)
}

// Handler answers one incoming call.
type Handler func(request *Request)

// Request is one incoming call; stream frames from the caller arrive on In.
type Request struct {
	ID   uint64
	Call string
	Body Raw
	In   <-chan *Envelope

	gone    <-chan struct{} // closed when the handler returns
	session *Session
}

// Session is the session the request came on.
func (request *Request) Session() *Session { return request.session }

// OK replies ok with v.
func (request *Request) OK(value any) error {
	return request.session.connection.Send(&Envelope{ID: request.ID, OK: MustRaw(value)})
}

// Data sends a stream item for this request.
func (request *Request) Data(value any) error {
	return request.session.connection.Send(&Envelope{ID: request.ID, Data: MustRaw(value)})
}

// End ends this request's stream.
func (request *Request) End(value any) error {
	return request.session.connection.Send(&Envelope{ID: request.ID, End: MustRaw(value)})
}

// Fail sends a reject; a non-reject error becomes invalid.
func (request *Request) Fail(problem error) error {
	var reject *Reject
	if !errors.As(problem, &reject) {
		reject = &Reject{Reason: INVALID, Message: problem.Error()}
	}

	return request.session.connection.Send(&Envelope{ID: request.ID, Reject: reject})
}

// IsCanceled reports, without waiting, whether the caller sent cancel for this stream (§4).
func (request *Request) IsCanceled() bool {
	select {
	case envelope := <-request.In:
		{
			return envelope.Cancel != nil
		}
	default:
		{
			return false
		}
	}
}

// Next waits for the caller's next stream frame.
func (request *Request) Next() (*Envelope, error) {
	select {
	case envelope := <-request.In:
		return envelope, nil
	case <-request.gone:
		return nil, ErrClosed
	case <-request.session.done:
		return nil, ErrClosed
	}
}

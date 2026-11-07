package kit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// FakeNode is an in-memory node. It runs the kit cmds (kit/cmds) without a shell,
// keeps spaces in memory, and checks every frame the hub sends.
type FakeNode struct {
	Key     wire.Keypair
	Peers   wire.Peers    // hub IP → hub key
	Tags    []string      // default [kit]
	Slots   int           // default 2
	Expires time.Duration // 0 = never
	Hub     string        // ip:port to send beat and post; "" = no signal
	HubKey  wire.Key
	Report  *Report // where hub violations go; nil = drop them

	mutex    sync.Mutex
	jobs     map[string]*fjob
	calls    map[string]*callStat
	listener net.Listener
	posts    chan postItem
	stop     chan struct{}
}

type callStat struct {
	count int
	fail  int
}

type fjob struct {
	id      string
	mutex   sync.Mutex
	files   map[string]wire.Entry
	partial map[string][]byte
	events  []wire.Event
	state   wire.State
	kill    chan struct{}
	changed chan struct{}
	dropped bool
	used    time.Time
}

type postItem struct {
	job   string
	event wire.Event
}

// Serve accepts hub connections on ln until Close.
func (fakeNode *FakeNode) Serve(listener net.Listener) error {
	fakeNode.init()
	fakeNode.listener = listener
	if fakeNode.Hub != "" {
		go fakeNode.signal()
	}
	go fakeNode.janitor()
	for {
		socket, acceptError := listener.Accept()
		if acceptError != nil {
			return acceptError
		}
		go fakeNode.conn(socket)
	}
}

func (fakeNode *FakeNode) init() {
	if fakeNode.Tags == nil {
		fakeNode.Tags = []string{"kit"}
	}
	if fakeNode.Slots == 0 {
		fakeNode.Slots = 2
	}
	fakeNode.jobs = map[string]*fjob{}
	fakeNode.calls = map[string]*callStat{}
	fakeNode.posts = make(chan postItem, 1024)
	fakeNode.stop = make(chan struct{})
}

// Close stops serving; jobs stay in memory.
func (fakeNode *FakeNode) Close() error {
	close(fakeNode.stop)

	return fakeNode.listener.Close()
}

func (fakeNode *FakeNode) conn(socket net.Conn) {
	connection, serverError := wire.NewServer(socket, fakeNode.Key, fakeNode.Peers, nil)
	if serverError != nil {
		socket.Close()

		return
	}
	session := wire.NewSession(connection)
	session.OnBad = func(envelope *wire.Envelope, problem error) {
		fakeNode.bad("§4", "frame from the hub", fmt.Errorf("%v: %+v", problem, envelope))
	}
	_ = session.Run(fakeNode.handle)
}

func (fakeNode *FakeNode) bad(spec, name string, problem error) {
	if fakeNode.Report != nil {
		fakeNode.Report.Check(spec, name, problem)
	}
}

func (fakeNode *FakeNode) count(call string, problem error) {
	fakeNode.mutex.Lock()
	stat := fakeNode.calls[call]
	if stat == nil {
		stat = &callStat{}
		fakeNode.calls[call] = stat
	}
	stat.count++
	fakeNode.mutex.Unlock()
	if problem != nil && wire.IsReject(problem, wire.INVALID) {
		fakeNode.mutex.Lock()
		stat.fail++
		fakeNode.mutex.Unlock()
		fakeNode.bad("§6", "hub "+call+" body", problem)
	}
}

// Summary adds one result per call the hub made, and whether it dropped its jobs.
func (fakeNode *FakeNode) Summary(report *Report) {
	fakeNode.mutex.Lock()
	defer fakeNode.mutex.Unlock()

	names := make([]string, 0, len(fakeNode.calls))
	for key := range fakeNode.calls {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		stat := fakeNode.calls[key]
		var problem error
		if stat.fail > 0 {
			problem = fmt.Errorf("%d of %d invalid", stat.fail, stat.count)
		}
		report.Check("§6", fmt.Sprintf("hub %s ×%d", key, stat.count), problem)
	}
	if len(names) == 0 {
		report.Check("§6", "hub made calls", errors.New("none"))

		return
	}
	left := 0
	for _, job := range fakeNode.jobs {
		job.mutex.Lock()
		if !job.dropped {
			left++
		}
		job.mutex.Unlock()
	}
	var problem error
	if left > 0 {
		problem = fmt.Errorf("%d left open", left)
	}
	report.Check("§6", "hub dropped its jobs", problem)
}

func (fakeNode *FakeNode) handle(request *wire.Request) {
	var problem error
	switch request.Call {
	case wire.CALL_OPEN:
		{
			problem = fakeNode.open(request)
		}
	case wire.CALL_PUSH:
		{
			problem = fakeNode.push(request)
		}
	case wire.CALL_EXEC:
		{
			problem = fakeNode.exec(request)
		}
	case wire.CALL_TAIL:
		{
			problem = fakeNode.tail(request)
		}
	case wire.CALL_STAT:
		{
			problem = fakeNode.withJob(request, func(job *fjob) error { return request.OK(job.state) })
		}
	case wire.CALL_KILL:
		{
			problem = fakeNode.withJob(request, func(job *fjob) error {
				if job.kill != nil {
					close(job.kill)
					job.kill = nil
				}

				return request.OK(wire.Empty{})
			})
		}
	case wire.CALL_PULL:
		{
			problem = fakeNode.pull(request)
		}
	case wire.CALL_DROP:
		{
			problem = fakeNode.drop(request)
		}
	default:
		{
			problem = wire.NewReject(wire.INVALID, "unknown call %q", request.Call)
		}
	}
	fakeNode.count(request.Call, problem)
	if problem != nil {
		_ = request.Fail(problem)
	}
}

func (fakeNode *FakeNode) job(id string) (*fjob, error) {
	checkJobError := wire.CheckJob(id)
	if checkJobError != nil {
		return nil, checkJobError
	}
	fakeNode.mutex.Lock()
	job := fakeNode.jobs[id]
	fakeNode.mutex.Unlock()
	if job != nil {
		job.mutex.Lock()
		gone := job.dropped
		job.mutex.Unlock()
		if gone {
			job = nil
		}
	}
	if job == nil {
		return nil, wire.NewReject(wire.UNKNOWN, "no job %s", id)
	}
	return job, nil
}

// withJob decodes {job}, locks the job, and runs fn.
func (fakeNode *FakeNode) withJob(request *wire.Request, callback func(job *fjob) error) error {
	var body wire.JobRef
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	job, jobError := fakeNode.job(body.Job)
	if jobError != nil {
		return jobError
	}
	job.mutex.Lock()
	defer job.mutex.Unlock()

	job.used = time.Now()

	return callback(job)
}

func (fakeNode *FakeNode) open(request *wire.Request) error {
	var body wire.Open
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	if !contains(fakeNode.Tags, body.Tag) {
		return wire.NewReject(wire.UNKNOWN, "tag %q not served", body.Tag)
	}
	fakeNode.mutex.Lock()
	count := fakeNode.usedLocked()
	if count >= fakeNode.Slots {
		fakeNode.mutex.Unlock()

		return wire.NewReject(wire.FULL, "%d of %d slots used", count, fakeNode.Slots)
	}
	id := make([]byte, 16)
	rand.Read(id)
	job := &fjob{id: hex.EncodeToString(id), files: map[string]wire.Entry{}, partial: map[string][]byte{},
		state: wire.State{State: wire.STATE_IDLE}, changed: make(chan struct{}), used: time.Now()}
	fakeNode.jobs[job.id] = job
	fakeNode.mutex.Unlock()

	return request.OK(wire.OpenOK{Job: job.id, Expires: uint64(fakeNode.Expires.Milliseconds())})
}

func (fakeNode *FakeNode) drop(request *wire.Request) error {
	return fakeNode.withJob(request, func(job *fjob) error {
		fakeNode.dropLocked(job)

		return request.OK(wire.Empty{})
	})
}

// usedLocked counts the jobs not dropped; the caller holds fakeNode.mutex.
func (fakeNode *FakeNode) usedLocked() int {
	count := 0
	for _, job := range fakeNode.jobs {
		job.mutex.Lock()
		if !job.dropped {
			count++
		}
		job.mutex.Unlock()
	}

	return count
}

func (fakeNode *FakeNode) dropLocked(job *fjob) {
	if job.kill != nil {
		close(job.kill)
		job.kill = nil
	}
	job.dropped = true
	// emptied, not nil: a push or cmd still on its way may write here
	job.files, job.partial, job.events = map[string]wire.Entry{}, map[string][]byte{}, nil
	close(job.changed)
	job.changed = make(chan struct{})
}

func (fakeNode *FakeNode) janitor() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-fakeNode.stop:
			return
		case <-ticker.C:
		}
		if fakeNode.Expires == 0 {
			continue
		}
		fakeNode.mutex.Lock()
		for _, job := range fakeNode.jobs {
			job.mutex.Lock()
			if !job.dropped && job.state.State != wire.STATE_RUNNING && time.Since(job.used) > fakeNode.Expires {
				fakeNode.dropLocked(job)
			}
			job.mutex.Unlock()
		}
		fakeNode.mutex.Unlock()
	}
}

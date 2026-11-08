package node

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/diphyx/grid/pkg/history"
	"github.com/diphyx/grid/pkg/wire"
)

const (
	MAX_LINE   = 8 * 1024         // §8: a longer log line is split
	KILL_WAIT  = 10 * time.Second // §9: SIGTERM, then SIGKILL
	DRAIN_WAIT = time.Second      // after the cmd exits, its output may still drain this long
)

// proc is a running exec.
type proc struct {
	command *exec.Cmd
	group   *group      // the cmd and whatever it starts
	stop    chan string // closed to stop it
	stopped string      // why, set before stop closes: kill, timeout, drop
	once    sync.Once
}

// halt stops the process group: SIGTERM, then SIGKILL after KillWait.
func (proc *proc) halt(why string) {
	proc.once.Do(func() {
		proc.stopped = why
		close(proc.stop)
	})
}

// start runs a cmd in the job's space; the caller holds job.mutex.
func (node *Node) start(job *Job, body wire.Exec, def Cmd) {
	argv := append(append([]string(nil), def.Exec...), body.Args...)
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = job.path("space")
	own := job.path("own")
	env := os.Environ()
	for key, value := range body.Env {
		env = append(env, key+"="+value)
	}
	env = append(env, "GRID_JOB="+job.ID(), "GRID_SPACE="+command.Dir, "GRID_EVENTS="+own)
	command.Env = env
	prepareGroup(command)

	run := Run{Cmd: body.Cmd, State: wire.STATE_RUNNING, Start: time.Now()}
	job.meta.Runs = append(job.meta.Runs, run)
	job.emit(wire.EVENT_STATE, wire.State{State: wire.STATE_RUNNING, Cmd: body.Cmd})

	pipes, startError := startCommand(command, own)
	if startError != nil {
		node.finish(job, wire.State{State: wire.STATE_FAILED, Cmd: body.Cmd, Message: startError.Error()})

		return
	}
	process := &proc{command: command, group: startGroup(command), stop: make(chan string)}
	job.proc = process
	node.running.Add(1)
	go node.watch(job, body.Cmd, def.Timeout.D(), process, pipes, own)
}

// startCommand makes the events file and the output pipes, and starts the command; the
// returned read ends are the caller's to close. Plain pipes, not exec's: Wait then returns
// when the cmd exits, even if something it left behind still holds its output.
func startCommand(command *exec.Cmd, own string) ([2]*os.File, error) {
	var pipes [2]*os.File
	ownError := os.WriteFile(own, nil, 0o644)
	if ownError != nil {
		return pipes, ownError
	}
	var writers [2]*os.File
	for i := range pipes {
		reader, writer, pipeError := os.Pipe()
		if pipeError != nil {
			closeAll(pipes[:], writers[:])

			return [2]*os.File{}, pipeError
		}
		pipes[i], writers[i] = reader, writer
	}
	command.Stdout, command.Stderr = writers[0], writers[1]
	startError := command.Start()
	closeAll(writers[:]) // the cmd has its own copies now
	if startError != nil {
		closeAll(pipes[:])

		return [2]*os.File{}, startError
	}

	return pipes, nil
}

func closeAll(lists ...[]*os.File) {
	for _, list := range lists {
		for _, file := range list {
			if file != nil {
				_ = file.Close()
			}
		}
	}
}

func (node *Node) watch(job *Job, name string, timeout time.Duration, process *proc, pipes [2]*os.File, own string) {
	var readers sync.WaitGroup
	var lastErr string
	var lmu sync.Mutex
	logLines := func(source io.Reader, stream string) {
		defer readers.Done()

		readLines(source, func(text string) {
			job.mutex.Lock()
			job.emit(wire.EVENT_LOG, wire.Log{Stream: stream, Line: text})
			job.mutex.Unlock()
			if stream == "err" && strings.TrimSpace(text) != "" {
				lmu.Lock()
				lastErr = text
				lmu.Unlock()
			}
		})
	}
	readers.Add(2)
	go logLines(pipes[0], "out")
	go logLines(pipes[1], "err")

	ownDone := make(chan struct{})
	ownStop := make(chan struct{})
	go func() {
		defer close(ownDone)

		node.readOwn(job, own, ownStop)
	}()

	var timer <-chan time.Time
	if timeout > 0 {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()

		timer = deadline.C
	}
	exited := make(chan error, 1)
	go func() { exited <- process.command.Wait() }()

	// the reason is read only after halt or a closed stop, never racing a late halt
	var problem error
	stopped := ""
	select {
	case problem = <-exited:
	case <-timer:
		process.halt(wire.STATE_STOPPED)
		stopped = process.stopped
	case <-process.stop:
		stopped = process.stopped
	}
	defer process.group.close()

	if stopped != "" {
		process.group.terminate()
		select {
		case problem = <-exited:
		case <-time.After(KILL_WAIT):
			process.group.kill()
			problem = <-exited
		}
	}
	// the cmd has exited: whatever it left running in its group goes too (the group's id is
	// not reused while any of it lives), then its output drains
	process.group.kill()
	drained := make(chan struct{})
	go func() {
		readers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(DRAIN_WAIT):
		closeAll(pipes[:]) // held by something that left the group
		<-drained
	}
	closeAll(pipes[:])
	close(ownStop)
	<-ownDone

	end := wire.State{State: wire.STATE_DONE, Cmd: name}
	switch {
	case stopped != "":
		{
			end.State = wire.STATE_STOPPED
		}
	case problem != nil:
		{
			end.State = wire.STATE_FAILED
			lmu.Lock()
			end.Message = lastErr
			lmu.Unlock()
			if end.Message == "" {
				end.Message = problem.Error()
			}
		}
	}
	job.mutex.Lock()
	job.proc = nil
	if !node.closing.Load() {
		node.finish(job, end)
	}
	job.mutex.Unlock()
	node.running.Done()
}

// readLines calls line for each line of source, a longer one split in parts of up to
// MAX_LINE bytes, on character boundaries; text that is not UTF-8 is made so (§5).
func readLines(source io.Reader, line func(string)) {
	reader := bufio.NewReaderSize(source, MAX_LINE-utf8.UTFMax)
	var carry []byte // the start of a character cut off at the end of a part
	isAfterSplit := false
	for {
		slice, readError := reader.ReadSlice('\n')
		isFull := errors.Is(readError, bufio.ErrBufferFull)
		chunk := append(carry, slice...)
		carry = nil
		if isFull {
			chunk, carry = cutRune(chunk)
		}
		text := strings.TrimSuffix(string(chunk), "\n")
		// a newline right after a split part ends that line; it is not an empty one
		isEmptyAfterSplit := isAfterSplit && text == "" && len(slice) == 1
		if (len(slice) > 0 || text != "") && !isEmptyAfterSplit && (isFull || text != "" || readError == nil) {
			line(strings.ToValidUTF8(text, "\uFFFD"))
		}
		isAfterSplit = isFull
		if readError != nil && !isFull {
			return
		}
	}
}

// cutRune splits off a character cut short at the end of chunk.
func cutRune(chunk []byte) ([]byte, []byte) {
	for back := 1; back < utf8.UTFMax && back <= len(chunk); back++ {
		start := len(chunk) - back
		if utf8.RuneStart(chunk[start]) {
			if utf8.FullRune(chunk[start:]) {
				return chunk, nil
			}

			return chunk[:start:start], append([]byte(nil), chunk[start:]...)
		}
	}

	return chunk, nil
}

// finish records the end of an exec; the caller holds job.mutex.
func (node *Node) finish(job *Job, end wire.State) {
	if job.dropped {
		return
	}
	index := len(job.meta.Runs) - 1
	if index >= 0 {
		run := &job.meta.Runs[index]
		run.State, run.Message, run.End = end.State, end.Message, time.Now()
		node.history.Add(history.Entry{At: run.End, Kind: history.KIND_JOB, Event: run.State, Subject: job.ID() + "/" + run.Cmd, Message: run.Message, Took: run.End.Sub(run.Start)})
	}
	job.meta.Used = time.Now()
	job.emit(wire.EVENT_STATE, end)
}

// readOwn turns event lines in $GRID_EVENTS into events (§8) until stop, then reads the rest.
// A line past EVENT_LINE_SIZE is reported cut short; a file written over starts again.
func (node *Node) readOwn(job *Job, path string, stop <-chan struct{}) {
	file, openError := os.Open(path)
	if openError != nil {
		return
	}
	defer file.Close()

	var pending []byte
	var offset int64
	isSkipping := false // the rest of a too long line
	chunk := make([]byte, 32*1024)
	drain := func() {
		info, statError := file.Stat()
		if statError == nil && info.Size() < offset {
			// truncated, as by `>` instead of `>>`: read it again from the start
			_, _ = file.Seek(0, io.SeekStart)
			offset, pending, isSkipping = 0, nil, false
		}
		for {
			index, readError := file.Read(chunk)
			offset += int64(index)
			pending = append(pending, chunk[:index]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				if !isSkipping {
					node.ownLine(job, pending[:i])
				}
				pending, isSkipping = pending[i+1:], false
			}
			if !isSkipping && len(pending) > wire.EVENT_LINE_SIZE {
				node.ownLine(job, pending) // too long: arrives as an err line
				pending, isSkipping = nil, true
			} else if isSkipping {
				pending = nil
			}
			if index == 0 || readError != nil {
				return
			}
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		drain()
		select {
		case <-stop:
			drain()
			if !isSkipping && len(bytes.TrimSpace(pending)) > 0 {
				node.ownLine(job, pending)
			}
			return
		case <-ticker.C:
		}
	}
}

func (node *Node) ownLine(job *Job, line []byte) {
	text := strings.TrimRight(string(line), "\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	name, pairs, parseError := wire.ParseEventLine(text)
	job.mutex.Lock()
	defer job.mutex.Unlock()

	if parseError == nil && !utf8.ValidString(text) {
		parseError = errors.New("not UTF-8")
	}
	if parseError != nil {
		if len(text) > MAX_LINE {
			text, _ = cutRuneString(text[:MAX_LINE])
		}
		job.emit(wire.EVENT_LOG, wire.Log{Stream: "err", Line: strings.ToValidUTF8(text, "\uFFFD")})

		return
	}
	if pairs == nil {
		job.emit(wire.EVENT_PREFIX+name, nil)

		return
	}
	job.emit(wire.EVENT_PREFIX+name, pairs)
}

func cutRuneString(text string) (string, string) {
	head, tail := cutRune([]byte(text))

	return string(head), string(tail)
}

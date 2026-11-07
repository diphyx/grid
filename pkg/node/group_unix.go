//go:build !windows

package node

import (
	"os/exec"
	"syscall"
)

// group is a cmd and everything it starts: on Unix, its own process group.
type group struct {
	id int
}

// prepareGroup makes the cmd lead a new process group; call it before Start.
func prepareGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// startGroup returns the group of a started cmd. Its id is not reused while any of it lives,
// so signals after the leader exits reach only what it left behind.
func startGroup(command *exec.Cmd) *group {
	return &group{id: command.Process.Pid}
}

// terminate asks the whole group to stop: SIGTERM.
func (group *group) terminate() { _ = syscall.Kill(-group.id, syscall.SIGTERM) }

// kill stops the whole group now: SIGKILL.
func (group *group) kill() { _ = syscall.Kill(-group.id, syscall.SIGKILL) }

// close frees what the group holds; nothing on Unix.
func (group *group) close() {}

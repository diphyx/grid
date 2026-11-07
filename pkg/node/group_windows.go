//go:build windows

package node

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// group is a cmd and everything it starts: on Windows, a job object, which also ends them all
// when the node lets go of it.
type group struct {
	job     windows.Handle
	process *os.Process // without a job object, at least the cmd itself
}

// prepareGroup gives the cmd its own console process group; call it before Start.
func prepareGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// startGroup puts a started cmd in a new job object; what it starts from then on joins it.
func startGroup(command *exec.Cmd) *group {
	created := &group{process: command.Process}
	job, createError := windows.CreateJobObject(nil, nil)
	if createError != nil {
		return created
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	process, openError := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if openError != nil {
		_ = windows.CloseHandle(job)

		return created
	}
	assignError := windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if assignError != nil {
		_ = windows.CloseHandle(job)

		return created
	}
	created.job = job

	return created
}

// terminate stops the whole group. Windows has no SIGTERM for a process without a console
// window, so it is the same as kill.
func (group *group) terminate() { group.kill() }

// kill stops the whole group now.
func (group *group) kill() {
	if group.job != 0 {
		_ = windows.TerminateJobObject(group.job, 1)

		return
	}
	_ = group.process.Kill()
}

// close lets go of the job object, which ends whatever is still in it.
func (group *group) close() {
	if group.job != 0 {
		_ = windows.CloseHandle(group.job)
		group.job = 0
	}
}

package hub

import (
	"errors"
	"fmt"
	"strings"

	"github.com/diphyx/grid/pkg/wire"
)

// Job is what the hub runs: a TOML file, any name (docs/job.md). One job runs on one node,
// from `open` to `drop`; if the node is lost, it runs again on another, up to Retries times.
type Job struct {
	Tag     string `toml:"tag" json:"tag"`
	Push    []Push `toml:"push" json:"push,omitempty"`
	Steps   []Step `toml:"steps" json:"steps"`
	Pull    []Pull `toml:"pull" json:"pull,omitempty"`
	Retries int    `toml:"retries" json:"retries,omitempty"`
}

// Push is a local file or folder to put into the space, under To.
type Push struct {
	From string `toml:"from" json:"from"`
	To   string `toml:"to" json:"to,omitempty"`
}

// Pull is a file or folder in the space to bring back to the hub, after the steps.
type Pull struct {
	Path string `toml:"path" json:"path"`
}

// Step is one exec of a cmd the node defines.
type Step struct {
	Cmd  string            `toml:"cmd" json:"cmd"`
	Args []string          `toml:"args" json:"args,omitempty"`
	Env  map[string]string `toml:"env" json:"env,omitempty"`
}

// ParseJob reads a job file, TOML; unknown keys are errors.
func ParseJob(content []byte) (*Job, error) {
	var job Job
	decodeError := wire.DecodeTOML(content, &job)
	if decodeError != nil {
		return nil, decodeError
	}
	checkError := job.Check()
	if checkError != nil {
		return nil, checkError
	}

	return &job, nil
}

// Check reports what is wrong with the job.
func (job *Job) Check() error {
	var errs []error
	if job.Tag == "" {
		errs = append(errs, errors.New("tag: required"))
	}
	if len(job.Steps) == 0 {
		errs = append(errs, errors.New("steps: at least one"))
	}
	for i, step := range job.Steps {
		if step.Cmd == "" {
			errs = append(errs, fmt.Errorf("steps[%d].cmd: required", i))
		}
		for key := range step.Env {
			if strings.HasPrefix(key, "GRID_") {
				errs = append(errs, fmt.Errorf("steps[%d].env: %s is reserved", i, key))
			}
		}
	}
	for i, push := range job.Push {
		if push.From == "" {
			errs = append(errs, fmt.Errorf("push[%d].from: required", i))
		}
		if push.To != "" && wire.CheckPath(push.To) != nil {
			errs = append(errs, fmt.Errorf("push[%d].to: %q leaves the space", i, push.To))
		}
	}
	for i, pull := range job.Pull {
		if pull.Path == "" {
			errs = append(errs, fmt.Errorf("pull[%d].path: required", i))
		} else if wire.CheckPath(pull.Path) != nil {
			errs = append(errs, fmt.Errorf("pull[%d].path: %q leaves the space", i, pull.Path))
		}
	}
	if job.Retries < 0 {
		errs = append(errs, errors.New("retries: not negative"))
	}

	return errors.Join(errs...)
}

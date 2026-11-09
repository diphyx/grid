package hub

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/diphyx/grid/v2/pkg/wire"
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

// Push is a local file or folder to put into the space, under To. A share push goes into the
// node's share, kept for the jobs after, and is linked at To.
type Push struct {
	From  string `toml:"from" json:"from"`
	To    string `toml:"to" json:"to,omitempty"`
	Share bool   `toml:"share" json:"share,omitempty"`
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
	errs = append(errs, job.checkShares()...)

	return errors.Join(errs...)
}

// checkShares checks what a share push needs: a To that is not the space, and no other push or
// pull at it or under it, since a link stands there. A push into a folder above it is checked
// at submit, against its tar.
func (job *Job) checkShares() []error {
	var errs []error
	for i, share := range job.Push {
		if !share.Share {
			continue
		}
		if share.To == "" || share.To == "." {
			errs = append(errs, fmt.Errorf("push[%d].to: a share needs one, not the space", i))

			continue
		}
		for other, push := range job.Push {
			if other != i && isUnder(spacePath(push.To), share.To) {
				errs = append(errs, fmt.Errorf("push[%d].to: %q is in push[%d], a share", other, push.To, i))
			}
		}
		for other, pull := range job.Pull {
			if isUnder(pull.Path, share.To) || isUnder(share.To, pull.Path) {
				errs = append(errs, fmt.Errorf("pull[%d].path: %q takes in push[%d], a share", other, pull.Path, i))
			}
		}
	}

	return errs
}

// checkTars checks, against their tars, the pushes into a folder above a share push: none may
// bring an entry at the share's To or under it.
func (job *Job) checkTars(store *tars, sums []string) error {
	var errs []error
	for i, share := range job.Push {
		if !share.Share {
			continue
		}
		for other, push := range job.Push {
			to := spacePath(push.To)
			if other == i || !isUnder(share.To, to) {
				continue
			}
			inside := strings.TrimPrefix(share.To, to+"/")
			if to == "." {
				inside = share.To
			}
			names, readError := tarNames(store.path(sums[other]))
			if readError != nil {
				return fmt.Errorf("push[%d]: %w", other, readError)
			}
			for _, name := range names {
				if isUnder(name, inside) {
					errs = append(errs, fmt.Errorf("push[%d]: %q is in push[%d], a share", other, path.Join(to, name), i))

					break
				}
			}
		}
	}

	return errors.Join(errs...)
}

// tarNames lists the entries of a tar file, without reading their contents.
func tarNames(file string) ([]string, error) {
	opened, openError := os.Open(file)
	if openError != nil {
		return nil, openError
	}
	defer func() { _ = opened.Close() }()

	var names []string
	reader := tar.NewReader(opened)
	for {
		header, nextError := reader.Next()
		if errors.Is(nextError, io.EOF) {
			return names, nil
		}
		if nextError != nil {
			return nil, nextError
		}
		names = append(names, strings.TrimSuffix(header.Name, "/"))
	}
}

// spacePath is a push's To, "" being the space.
func spacePath(to string) string {
	if to == "" {
		return "."
	}

	return to
}

// isUnder reports whether path is folder or under it; "." holds every path.
func isUnder(path, folder string) bool {
	path, folder = strings.TrimSuffix(path, "/"), strings.TrimSuffix(folder, "/")

	return folder == "." || path == folder || strings.HasPrefix(path, folder+"/")
}

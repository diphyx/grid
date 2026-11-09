package hub_test

import (
	"fmt"

	"github.com/diphyx/grid/v2/pkg/hub"
)

// How to embed it in a Go program; compiled with the tests so it stays right.
func runOne() error {
	config, configError := hub.LoadConfig("/etc/grid/hub.toml") // or fill hub.Config in code
	if configError != nil {
		return configError
	}
	jobs, newError := hub.New(config, nil)
	if newError != nil {
		return newError
	}
	startError := jobs.Start() // reopens jobs on disk; listens for beat and post with Signal
	if startError != nil {
		return startError
	}
	defer jobs.Close()

	id, submitError := jobs.Submit(hub.Job{
		Tag:   "build",
		Push:  []hub.Push{{From: "./src"}},
		Steps: []hub.Step{{Cmd: "build", Args: []string{"linux"}}},
		Pull:  []hub.Pull{{Path: "out"}},
	}, "") // push paths are relative to this folder; "" = the current one
	if submitError != nil {
		return submitError
	}
	record, _ := jobs.Wait(id) // record.State: done, failed, or stopped
	fmt.Println(record.State)

	return nil
}

func Example() {
	_ = runOne
}

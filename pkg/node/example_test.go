package node_test

import "github.com/diphyx/grid/pkg/node"

// How to embed it in a Go program; compiled with the tests so it stays right.
func serve() error {
	config, configError := node.LoadConfig("/etc/grid/node.toml")
	if configError != nil {
		return configError
	}
	runner, newError := node.New(config, nil)
	if newError != nil {
		return newError
	}
	listenError := runner.Listen()
	if listenError != nil {
		return listenError
	}
	go runner.ServeLocal() // the API for the CLI; optional

	return runner.Serve() // until Close
}

func Example() {
	_ = serve
}

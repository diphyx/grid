package hub

import (
	"errors"
	"fmt"
	"os"

	"github.com/diphyx/grid/pkg/wire"
)

// Defaults.
const (
	DEFAULT_CONFIG = "/etc/grid/hub.toml"
	DEFAULT_DATA   = "/var/lib/grid/hub"
	DEFAULT_SOCKET = "/run/grid/hub.sock"
)

// Config is the hub config file, TOML (docs/hub.md); an embedding project fills it in code.
type Config struct {
	Private string        `toml:"private"`
	Signal  bool          `toml:"signal"`
	Port    int           `toml:"port"`
	Nodes   []Peer        `toml:"nodes"`
	History wire.Duration `toml:"history"` // how long finished jobs and their files stay; 0 = forever
	Data    string        `toml:"data"`
	Socket  string        `toml:"socket"`
}

// Peer is one node the hub may use.
type Peer struct {
	Tags []string `toml:"tags"`
	IP   string   `toml:"ip"`
	Port int      `toml:"port"`
	Key  string   `toml:"key"`
}

// LoadConfig reads and checks a config file.
func LoadConfig(path string) (*Config, error) {
	content, readError := os.ReadFile(path)
	if readError != nil {
		return nil, readError
	}
	var config Config
	decodeError := wire.DecodeTOML(content, &config)
	if decodeError != nil {
		return nil, fmt.Errorf("%s: %w", path, decodeError)
	}
	checkError := config.check()
	if checkError != nil {
		return nil, fmt.Errorf("%s: %w", path, checkError)
	}

	return &config, nil
}

// check fills defaults and reports what is missing.
func (config *Config) check() error {
	if config.Data == "" {
		config.Data = DEFAULT_DATA
	}
	if config.Socket == "" {
		config.Socket = DEFAULT_SOCKET
	}
	var errs []error
	if config.Private == "" {
		errs = append(errs, errors.New("private: required"))
	}
	if len(config.Nodes) == 0 {
		errs = append(errs, errors.New("nodes: at least one"))
	}
	ips := map[string]int{}
	for i, peer := range config.Nodes {
		// with signal, a node is known by the IP it connects from
		first, isTaken := ips[wire.NormIP(peer.IP)]
		if config.Signal && isTaken && peer.IP != "" {
			errs = append(errs, fmt.Errorf("nodes[%d]: ip %s is also nodes[%d]; with signal each node needs its own ip", i, peer.IP, first))
		} else if !isTaken {
			ips[wire.NormIP(peer.IP)] = i
		}
		if peer.IP == "" || peer.Key == "" {
			errs = append(errs, fmt.Errorf("nodes[%d]: ip and key required", i))
		}
		if len(peer.Tags) == 0 {
			errs = append(errs, fmt.Errorf("nodes[%d]: at least one tag", i))
		}
	}

	errs = append(errs, config.History.Check("history"))

	return errors.Join(errs...)
}

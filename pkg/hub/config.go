package hub

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// Defaults.
const (
	DEFAULT_CONFIG  = "/etc/grid/hub.toml"
	DEFAULT_JOBS    = "/var/lib/grid/hub/jobs"
	DEFAULT_HISTORY = "/var/lib/grid/hub/history"
	DEFAULT_SOCKET  = "/run/grid/hub.sock"

	DEFAULT_JOBS_EXPIRES  = wire.Duration(30 * 24 * time.Hour)
	DEFAULT_HISTORY_LIMIT = 100 * wire.MB
)

// Config is the hub config file, TOML (docs/hub.md); an embedding project fills it in code.
type Config struct {
	Identity string  `toml:"identity"`
	Signal   bool    `toml:"signal"`
	Port     int     `toml:"port"`
	Socket   string  `toml:"socket"`
	Jobs     Jobs    `toml:"jobs"`
	History  History `toml:"history"`
	Nodes    []Peer  `toml:"nodes"`
}

// Jobs is where the jobs live and how long finished ones stay.
type Jobs struct {
	Path    string         `toml:"path"`
	Expires *wire.Duration `toml:"expires"` // a finished job and its files go after this; default 30d; 0 = never
}

// History is the hub's event log.
type History struct {
	Path    string        `toml:"path"`
	Limit   *wire.Size    `toml:"limit"`   // default 100MB; 0 = off
	Expires wire.Duration `toml:"expires"` // entries older are dropped; 0 = never
}

// Peer is one node the hub may use.
type Peer struct {
	Tags  []string `toml:"tags"`
	IP    string   `toml:"ip"`
	Port  int      `toml:"port"`
	Trust string   `toml:"trust"`
	Share bool     `toml:"share"` // a hint that its share is on; its push replies and beats say for sure
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
	if config.Socket == "" {
		config.Socket = DEFAULT_SOCKET
	}
	if config.Jobs.Path == "" {
		config.Jobs.Path = DEFAULT_JOBS
	}
	if config.Jobs.Expires == nil {
		expires := DEFAULT_JOBS_EXPIRES
		config.Jobs.Expires = &expires
	}
	if config.History.Path == "" {
		config.History.Path = DEFAULT_HISTORY
	}
	if config.History.Limit == nil {
		config.History.Limit = &wire.Size{Bytes: DEFAULT_HISTORY_LIMIT}
	}
	var errs []error
	if config.Identity == "" {
		errs = append(errs, errors.New("identity: required"))
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
		if peer.IP == "" || peer.Trust == "" {
			errs = append(errs, fmt.Errorf("nodes[%d]: ip and trust required", i))
		}
		if len(peer.Tags) == 0 {
			errs = append(errs, fmt.Errorf("nodes[%d]: at least one tag", i))
		}
	}

	errs = append(errs, config.Jobs.Expires.Check("jobs.expires"), config.History.Expires.Check("history.expires"))

	return errors.Join(errs...)
}

package node

import (
	"errors"
	"fmt"
	"os"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// Config is the node config file, TOML (docs/node.md).
type Config struct {
	Grid     int            `toml:"grid"` // the generation it is for: wire.MAJOR; checked in LoadConfig
	Identity string         `toml:"identity"`
	Port     int            `toml:"port"`
	Tags     []string       `toml:"tags"`
	Signal   bool           `toml:"signal"`
	Socket   string         `toml:"socket"` // default /run/grid/node.sock
	Hubs     []Peer         `toml:"hubs"`
	Jobs     Jobs           `toml:"jobs"`
	History  History        `toml:"history"`
	Share    Share          `toml:"share"`
	Cmds     map[string]Cmd `toml:"cmds"`
}

// Peer is a hub the node trusts; its jobs are its own.
type Peer struct {
	IP    string `toml:"ip"`
	Port  int    `toml:"port"`
	Trust string `toml:"trust"`
}

// Jobs is where the jobs live and how many run at once.
type Jobs struct {
	Path    string        `toml:"path"`
	Limit   int           `toml:"limit"`   // jobs at once; default 1
	Expires wire.Duration `toml:"expires"` // an idle job is dropped after this; 0 = never
}

// History is the node's event log.
type History struct {
	Path    string        `toml:"path"`
	Limit   *wire.Size    `toml:"limit"`   // default 100MB; 0 = off
	Expires wire.Duration `toml:"expires"` // entries older are dropped; 0 = never
}

// Share is the space for shared pushes.
type Share struct {
	Path    string        `toml:"path"`
	Limit   wire.Size     `toml:"limit"`   // default 0 = off
	Expires wire.Duration `toml:"expires"` // a share no hit for this long is removed; 0 = never
}

// Cmd is one cmd the node offers.
type Cmd struct {
	Exec    []string      `toml:"exec"`
	Timeout wire.Duration `toml:"timeout"`
}

// Defaults.
const (
	DEFAULT_JOBS    = wire.STATE + "/node/jobs"
	DEFAULT_HISTORY = wire.STATE + "/node/history"
	DEFAULT_SHARE   = wire.STATE + "/node/share"
	DEFAULT_SOCKET  = "/run/grid/node.sock"
	DEFAULT_CONFIG  = "/etc/grid/node.toml"

	DEFAULT_HISTORY_LIMIT = 100 * wire.MB
)

// LoadConfig reads and checks a config file.
func LoadConfig(path string) (*Config, error) {
	content, readFileError := os.ReadFile(path)
	if readFileError != nil {
		return nil, readFileError
	}
	majorError := wire.CheckMajor(content)
	if majorError != nil {
		return nil, fmt.Errorf("%s: %w", path, majorError)
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

func (config *Config) check() error {
	if config.Socket == "" {
		config.Socket = DEFAULT_SOCKET
	}
	if config.Jobs.Path == "" {
		config.Jobs.Path = DEFAULT_JOBS
	}
	if config.Jobs.Limit == 0 {
		config.Jobs.Limit = 1
	}
	if config.History.Path == "" {
		config.History.Path = DEFAULT_HISTORY
	}
	if config.History.Limit == nil {
		config.History.Limit = &wire.Size{Bytes: DEFAULT_HISTORY_LIMIT}
	}
	if config.Share.Path == "" {
		config.Share.Path = DEFAULT_SHARE
	}
	var errs []error
	if config.Identity == "" {
		errs = append(errs, errors.New("identity: required"))
	}
	if len(config.Tags) == 0 {
		errs = append(errs, errors.New("tags: at least one"))
	}
	if len(config.Hubs) == 0 {
		errs = append(errs, errors.New("hubs: at least one"))
	}
	seen := map[string]bool{}
	for index, peer := range config.Hubs {
		if peer.IP == "" || peer.Trust == "" {
			errs = append(errs, fmt.Errorf("hubs[%d]: ip and trust required", index))

			continue
		}
		ip := wire.NormIP(peer.IP)
		if seen[ip] {
			errs = append(errs, fmt.Errorf("hubs[%d]: ip %s twice", index, peer.IP))
		}
		seen[ip] = true
	}
	if config.Jobs.Limit < 1 {
		errs = append(errs, errors.New("jobs.limit: at least 1"))
	}
	for name, command := range config.Cmds {
		if len(command.Exec) == 0 {
			errs = append(errs, fmt.Errorf("cmds.%s.exec: required", name))
		}
	}

	errs = append(errs, config.Jobs.Expires.Check("jobs.expires"), config.History.Expires.Check("history.expires"), config.Share.Expires.Check("share.expires"))
	for name, command := range config.Cmds {
		errs = append(errs, command.Timeout.Check("cmds."+name+".timeout"))
	}

	return errors.Join(errs...)
}

// Keys loads the node's key pair and each hub's key, in the order of Hubs.
func (config *Config) Keys() (wire.Keypair, []wire.Key, error) {
	keypair, loadPrivateError := wire.LoadPrivate(config.Identity)
	if loadPrivateError != nil {
		return keypair, nil, fmt.Errorf("identity: %w", loadPrivateError)
	}
	keys := make([]wire.Key, len(config.Hubs))
	for index, peer := range config.Hubs {
		hubKey, loadKeyError := wire.LoadKey(peer.Trust)
		if loadKeyError != nil {
			return keypair, nil, fmt.Errorf("hubs[%d].trust: %w", index, loadKeyError)
		}
		keys[index] = hubKey
	}

	return keypair, keys, nil
}

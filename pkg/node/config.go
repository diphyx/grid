package node

import (
	"errors"
	"fmt"
	"os"

	"github.com/diphyx/grid/pkg/wire"
)

// Config is the node config file, TOML (docs/node.md).
type Config struct {
	Identity string         `toml:"identity"`
	Port     int            `toml:"port"`
	Tags     []string       `toml:"tags"`
	Signal   bool           `toml:"signal"`
	Socket   string         `toml:"socket"` // default /run/grid/node.sock
	Hub      Peer           `toml:"hub"`
	Jobs     Jobs           `toml:"jobs"`
	History  History        `toml:"history"`
	Share    Share          `toml:"share"`
	Cmds     map[string]Cmd `toml:"cmds"`
}

// Peer is the hub the node trusts.
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
	DEFAULT_JOBS    = "/var/lib/grid/node/jobs"
	DEFAULT_HISTORY = "/var/lib/grid/node/history"
	DEFAULT_SHARE   = "/var/lib/grid/node/share"
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
	if config.Hub.IP == "" || config.Hub.Trust == "" {
		errs = append(errs, errors.New("hub: ip and trust required"))
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

// Keys loads the node's key pair and the hub's key.
func (config *Config) Keys() (wire.Keypair, wire.Key, error) {
	keypair, loadPrivateError := wire.LoadPrivate(config.Identity)
	if loadPrivateError != nil {
		return keypair, wire.Key{}, fmt.Errorf("identity: %w", loadPrivateError)
	}
	hubKey, loadKeyError := wire.LoadKey(config.Hub.Trust)
	if loadKeyError != nil {
		return keypair, hubKey, fmt.Errorf("hub.trust: %w", loadKeyError)
	}
	return keypair, hubKey, nil
}

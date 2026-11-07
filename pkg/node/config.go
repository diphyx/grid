package node

import (
	"errors"
	"fmt"
	"os"

	"github.com/diphyx/grid/pkg/wire"
)

// Config is the node config file, TOML (docs/node.md).
type Config struct {
	Private string         `toml:"private"`
	Port    int            `toml:"port"`
	Tags    []string       `toml:"tags"`
	Slots   int            `toml:"slots"`
	Expires wire.Duration  `toml:"expires"`
	History wire.Duration  `toml:"history"`
	Signal  bool           `toml:"signal"`
	Hub     Peer           `toml:"hub"`
	Cmds    map[string]Cmd `toml:"cmds"`
	Data    string         `toml:"data"`   // default /var/lib/grid
	Socket  string         `toml:"socket"` // default /run/grid/node.sock
}

// Peer is the hub the node trusts.
type Peer struct {
	IP   string `toml:"ip"`
	Port int    `toml:"port"`
	Key  string `toml:"key"`
}

// Cmd is one cmd the node offers.
type Cmd struct {
	Exec    []string      `toml:"exec"`
	Timeout wire.Duration `toml:"timeout"`
}

// Defaults.
const (
	DEFAULT_DATA   = "/var/lib/grid"
	DEFAULT_SOCKET = "/run/grid/node.sock"
	DEFAULT_CONFIG = "/etc/grid/node.toml"
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
	if config.Data == "" {
		config.Data = DEFAULT_DATA
	}
	if config.Socket == "" {
		config.Socket = DEFAULT_SOCKET
	}
	if config.Slots == 0 {
		config.Slots = 1
	}
	var errs []error
	if config.Private == "" {
		errs = append(errs, errors.New("private: required"))
	}
	if len(config.Tags) == 0 {
		errs = append(errs, errors.New("tags: at least one"))
	}
	if config.Hub.IP == "" || config.Hub.Key == "" {
		errs = append(errs, errors.New("hub: ip and key required"))
	}
	for name, command := range config.Cmds {
		if len(command.Exec) == 0 {
			errs = append(errs, fmt.Errorf("cmds.%s.exec: required", name))
		}
	}

	errs = append(errs, config.Expires.Check("expires"), config.History.Check("history"))
	for name, command := range config.Cmds {
		errs = append(errs, command.Timeout.Check("cmds."+name+".timeout"))
	}

	return errors.Join(errs...)
}

// Keys loads the node's key pair and the hub's key.
func (config *Config) Keys() (wire.Keypair, wire.Key, error) {
	keypair, loadPrivateError := wire.LoadPrivate(config.Private)
	if loadPrivateError != nil {
		return keypair, wire.Key{}, fmt.Errorf("private: %w", loadPrivateError)
	}
	hubKey, loadPrivateError := wire.LoadKey(config.Hub.Key)
	if loadPrivateError != nil {
		return keypair, hubKey, fmt.Errorf("hub.key: %w", loadPrivateError)
	}
	return keypair, hubKey, nil
}

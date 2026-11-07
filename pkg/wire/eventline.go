package wire

import (
	"errors"
	"fmt"
	"strings"
)

// Event lines (§8): what a cmd writes to $GRID_EVENTS.
//
//	[name] key=value key="a value with spaces"
//
// The name is required; the pairs are optional. The node sends it as an event of type
// EVENT_PREFIX + name, with the pairs as its data, so a cmd's names never collide with grid's own.
const (
	EVENT_PREFIX    = "evt."
	EVENT_LINE_SIZE = 8 * 1024
)

// ParseEventLine reads one event line into its name and pairs; nil pairs when it has none.
func ParseEventLine(line string) (string, map[string]string, error) {
	if len(line) > EVENT_LINE_SIZE {
		return "", nil, fmt.Errorf("event line: %d bytes, over %d", len(line), EVENT_LINE_SIZE)
	}
	if !strings.HasPrefix(line, "[") {
		return "", nil, errors.New("event line: must start with [name]")
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", nil, errors.New("event line: no ] after the name")
	}
	name := line[1:end]
	if !nameRe.MatchString(name) {
		return "", nil, fmt.Errorf("event line: name %q", name)
	}
	rest := line[end+1:]
	if rest != "" && rest[0] != ' ' {
		return "", nil, errors.New("event line: a space must follow [name]")
	}
	pairs, pairsError := parsePairs(rest)
	if pairsError != nil {
		return "", nil, pairsError
	}

	return name, pairs, nil
}

// parsePairs reads space-separated key=value pairs; a value may be "quoted", with \" and \\.
func parsePairs(text string) (map[string]string, error) {
	var pairs map[string]string
	i := 0
	for {
		for i < len(text) && text[i] == ' ' {
			i++
		}
		if i == len(text) {
			return pairs, nil
		}
		equals := strings.IndexByte(text[i:], '=')
		if equals < 0 {
			return nil, fmt.Errorf("event line: %q is not key=value", strings.Fields(text[i:])[0])
		}
		key := text[i : i+equals]
		if !nameRe.MatchString(key) {
			return nil, fmt.Errorf("event line: key %q", key)
		}
		i += equals + 1
		var value strings.Builder
		if i < len(text) && text[i] == '"' {
			i++
			isClosed := false
			for i < len(text) {
				if text[i] == '\\' && i+1 < len(text) && (text[i+1] == '"' || text[i+1] == '\\') {
					value.WriteByte(text[i+1])
					i += 2

					continue
				}
				if text[i] == '"' {
					i++
					isClosed = true

					break
				}
				value.WriteByte(text[i])
				i++
			}
			if !isClosed {
				return nil, fmt.Errorf("event line: value of %q has no closing quote", key)
			}
			if i < len(text) && text[i] != ' ' {
				return nil, fmt.Errorf("event line: a space must follow the value of %q", key)
			}
		} else {
			for i < len(text) && text[i] != ' ' {
				if text[i] == '"' {
					return nil, fmt.Errorf("event line: a quote inside the value of %q", key)
				}
				value.WriteByte(text[i])
				i++
			}
		}
		if pairs == nil {
			pairs = map[string]string{}
		}
		pairs[key] = value.String()
	}
}

package wire

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Duration is a config duration, a string: "10m", "1h", "30d", or "0" (never). On the wire,
// durations are ms.
type Duration time.Duration

// UnmarshalText reads a duration such as "10m" or "30d".
func (duration *Duration) UnmarshalText(content []byte) error {
	parsed, parseDurationError := ParseDuration(string(content))
	if parseDurationError != nil {
		return parseDurationError
	}
	*duration = Duration(parsed)

	return nil
}

// Check catches a bare number in a config: TOML gives it as nanoseconds, so anything between 0
// and 1 ms is a number written without quotes.
func (duration Duration) Check(name string) error {
	if duration > 0 && time.Duration(duration) < time.Millisecond {
		return fmt.Errorf("%s: a duration is a string, like \"10m\"", name)
	}

	return nil
}

// DecodeTOML reads a config or job file into out; keys out has no field for are errors.
func DecodeTOML(content []byte, out any) error {
	decoder := toml.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	decodeError := decoder.Decode(out)
	var strict *toml.StrictMissingError
	if errors.As(decodeError, &strict) {
		unknown := make([]string, len(strict.Errors))
		for i, each := range strict.Errors {
			line, _ := each.Position()
			unknown[i] = fmt.Sprintf("%s (line %d)", strings.Join(each.Key(), "."), line)
		}

		return fmt.Errorf("unknown keys: %s", strings.Join(unknown, ", "))
	}
	var located *toml.DecodeError
	if errors.As(decodeError, &located) {
		line, column := located.Position()

		return fmt.Errorf("line %d, column %d: %w", line, column, decodeError)
	}

	return decodeError
}

// D is the duration as a time.Duration.
func (duration Duration) D() time.Duration { return time.Duration(duration) }

// ParseDuration is time.ParseDuration plus `d` for days.
func ParseDuration(input string) (time.Duration, error) {
	input = strings.TrimSpace(input)
	if input == "" || input == "0" {
		return 0, nil
	}
	text, ok := strings.CutSuffix(input, "d")
	if ok {
		days, atoiError := strconv.Atoi(text)
		if atoiError != nil {
			return 0, fmt.Errorf("duration %q", input)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return time.ParseDuration(input)
}

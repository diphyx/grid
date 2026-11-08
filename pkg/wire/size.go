package wire

import (
	"fmt"
	"strconv"
	"strings"
)

// Size units, binary: 1KB = 1024 bytes.
const (
	KB = int64(1) << 10
	MB = int64(1) << 20
	GB = int64(1) << 30
	TB = int64(1) << 40
)

// Size is a config size, a string: "512MB", "10GB", or "0" (off). A struct, so TOML hands every
// value, a bare number too, to UnmarshalText.
type Size struct {
	Bytes int64
}

// UnmarshalText reads a size such as "10GB".
func (size *Size) UnmarshalText(content []byte) error {
	parsed, parseSizeError := ParseSize(string(content))
	if parseSizeError != nil {
		return parseSizeError
	}
	size.Bytes = parsed

	return nil
}

// MarshalText writes the size back in its largest whole unit.
func (size Size) MarshalText() ([]byte, error) { return []byte(size.String()), nil }

// String is the size in its largest whole unit, like "10GB".
func (size Size) String() string {
	for _, unit := range []struct {
		name  string
		bytes int64
	}{{"TB", TB}, {"GB", GB}, {"MB", MB}, {"KB", KB}} {
		if size.Bytes >= unit.bytes && size.Bytes%unit.bytes == 0 {
			return strconv.FormatInt(size.Bytes/unit.bytes, 10) + unit.name
		}
	}

	return strconv.FormatInt(size.Bytes, 10) + "B"
}

// ParseSize reads a whole number and a unit, B, KB, MB, GB, or TB; "0" alone is 0.
func ParseSize(input string) (int64, error) {
	input = strings.TrimSpace(input)
	if input == "0" {
		return 0, nil
	}
	for _, unit := range []struct {
		name  string
		bytes int64
	}{{"TB", TB}, {"GB", GB}, {"MB", MB}, {"KB", KB}, {"B", 1}} {
		text, ok := strings.CutSuffix(input, unit.name)
		if !ok {
			continue
		}
		count, parseError := strconv.ParseInt(text, 10, 64)
		if parseError != nil || count < 0 || count > (1<<62)/unit.bytes {
			return 0, fmt.Errorf("size %q", input)
		}

		return count * unit.bytes, nil
	}

	return 0, fmt.Errorf("size %q: a size has a unit, like \"10GB\"", input)
}

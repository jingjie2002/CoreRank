package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// String returns the trimmed environment value, or fallback when the variable
// is unset or blank.
func String(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

// Bool accepts common boolean spellings and returns fallback for unset or
// unrecognized values.
func Bool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

// Int64 parses an int64 environment value and returns fallback when parsing
// fails or the variable is unset.
func Int64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// Duration parses a Go duration string such as "3s" or "150ms". When the
// variable is unset or invalid, fallback is returned.
func Duration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// DurationMillis parses a millisecond integer environment value. It exists for
// legacy settings that already use *_MS names.
func DurationMillis(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return time.Duration(parsed) * time.Millisecond
}

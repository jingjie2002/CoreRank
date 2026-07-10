package repository

import (
	"errors"
	"fmt"
	"strings"
)

const (
	DefaultMatchMode = "default"
	maxMatchModeLen  = 32
)

var (
	ErrInvalidIdentifier = errors.New("invalid identifier")
	ErrInvalidMatchMode  = errors.New("invalid match_mode")
)

// NormalizeMatchMode keeps Redis keys and metric labels bounded and stable.
func NormalizeMatchMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return DefaultMatchMode, nil
	}
	if len(value) > maxMatchModeLen {
		return "", fmt.Errorf("%w: length must be <= %d", ErrInvalidMatchMode, maxMatchModeLen)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':' {
			continue
		}
		return "", fmt.Errorf("%w: only letters, digits, underscore, hyphen and colon are allowed", ErrInvalidMatchMode)
	}
	return value, nil
}

func ValidateIdentifier(field, value string, maxLen int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidIdentifier, field)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%w: %s length must be <= %d", ErrInvalidIdentifier, field, maxLen)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == ':' || r == '.' {
			continue
		}
		return fmt.Errorf("%w: %s contains unsupported characters", ErrInvalidIdentifier, field)
	}
	return nil
}

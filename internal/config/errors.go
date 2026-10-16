package config

import (
	"fmt"
	"strings"
)

// FieldError is one validation failure at a JSON-style field path, e.g. "slots[0].route[1]".
// Messages never contain secret values.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationErrors is the error returned by Validate; it lists every failure found.
type ValidationErrors []FieldError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, fe := range v {
		parts[i] = fe.Field + ": " + fe.Message
	}
	return "invalid config: " + strings.Join(parts, "; ")
}

func (v *ValidationErrors) add(field, format string, args ...any) {
	*v = append(*v, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

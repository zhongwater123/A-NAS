// Package envconfig reads a binary's configuration from its environment the
// same way in every A-NAS process: github.com/caarlos0/env fills a struct from
// its `env` and `envDefault` tags, after values are trimmed and blank ones
// dropped, so that a blank line in an environment file means the default.
// Each binary then validates what it read in one place (#192).
package envconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Environment is the process environment as Parse expects it.
func Environment() map[string]string {
	return Clean(env.ToMap(os.Environ()))
}

// Clean trims every value and drops the blank ones.
func Clean(environment map[string]string) map[string]string {
	cleaned := make(map[string]string, len(environment))
	for name, value := range environment {
		if value = strings.TrimSpace(value); value != "" {
			cleaned[name] = value
		}
	}
	return cleaned
}

// Parse fills a T from environment, which tests pass in place of the
// process's own.
func Parse[T any](environment map[string]string) (T, error) {
	return env.ParseAsWithOptions[T](env.Options{Environment: Clean(environment)})
}

// AbsolutePath returns value cleaned, or an error naming the variable when
// it is not an absolute path. An empty value stays empty.
func AbsolutePath(name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", errors.New(name + " must be an absolute path")
	}
	return filepath.Clean(value), nil
}

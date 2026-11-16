package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// getwd is os.Getwd in production. It is a variable so a test can pin the branch
// where the process has no readable working directory (a deleted cwd, a container
// whose mount went away), which is a real startup shape and not an error worth
// reporting: the walk simply does nothing when there is nowhere to start.
var getwd = os.Getwd

// LoadDotEnv walks up at most 6 directories from the working directory looking
// for a .env file and exports every key it finds. Keys that are already present
// in the process environment are never overwritten, so real deployments keep
// full control over their configuration.
func LoadDotEnv() {
	dir, err := getwd()
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		if applyDotEnv(filepath.Join(dir, ".env")) {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func applyDotEnv(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value != "" && value[0] == '"' && value[len(value)-1] == '"' && len(value) > 1 {
			value = value[1 : len(value)-1]
		}
		if value != "" && value[0] == '\'' && value[len(value)-1] == '\'' && len(value) > 1 {
			value = value[1 : len(value)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return true
		}
	}
	return true
}

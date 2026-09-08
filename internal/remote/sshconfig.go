package remote

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Hosts lists the Host aliases in the user's OpenSSH configuration for
// shell completion of --remote: plain aliases only, following Include,
// skipping wildcard and negated patterns. A convenience, never a
// restriction — any target ssh accepts is valid. Unreadable files are
// skipped silently; completion has no error channel worth using.
func Hosts(configPath string) []string {
	var hosts []string
	seen := map[string]bool{}
	visited := map[string]bool{}
	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		if depth > 8 || visited[path] {
			return
		}
		visited[path] = true
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			keyword, args := parseConfigLine(scanner.Text())
			switch keyword {
			case "host":
				for _, pattern := range args {
					if strings.ContainsAny(pattern, "*?!") || seen[pattern] {
						continue
					}
					seen[pattern] = true
					hosts = append(hosts, pattern)
				}
			case "include":
				for _, pattern := range args {
					for _, included := range expandInclude(pattern, filepath.Dir(path)) {
						walk(included, depth+1)
					}
				}
			}
		}
	}
	walk(configPath, 0)
	return hosts
}

// parseConfigLine splits one ssh_config line into its lowercased keyword
// and arguments; comments and blank lines yield an empty keyword. The
// keyword may be separated from its arguments by whitespace or '='.
func parseConfigLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	sep := strings.IndexAny(line, " \t=")
	if sep < 0 {
		return strings.ToLower(line), nil
	}
	keyword := strings.ToLower(line[:sep])
	rest := strings.TrimLeft(line[sep:], " \t=")
	return keyword, strings.Fields(rest)
}

// expandInclude resolves an Include argument the way ssh does: a ~ maps
// to the home directory, a relative path is relative to ~/.ssh (the
// user's configuration directory), and globs expand.
func expandInclude(pattern, configDir string) []string {
	if strings.HasPrefix(pattern, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		pattern = filepath.Join(home, pattern[2:])
	} else if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(configDir, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

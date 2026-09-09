package monitor

import "testing"

// resolve is the naming contract: a table over (argv, short name).
func TestResolve(t *testing.T) {
	for name, tc := range map[string]struct {
		argv  []string
		short string
		want  string
	}{
		"login shell dash":        {[]string{"-zsh"}, "zsh", "zsh"},
		"native binary":           {[]string{"/usr/bin/nvim", "."}, "nvim", "nvim"},
		"node script":             {[]string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}, "node", "codex"},
		"node with options":       {[]string{"node", "--max-old-space-size=4096", "--no-warnings", "./cli.mjs"}, "node", "cli"},
		"bare interpreter":        {[]string{"node"}, "node", "node"},
		"python with options":     {[]string{"python3", "-u", "script.py"}, "python3", "script"},
		"env assignments":         {[]string{"env", "FOO=1", "BAR=2", "tool", "--flag"}, "env", "tool"},
		"shell -c pipeline":       {[]string{"sh", "-c", "git log | less"}, "sh", "git"},
		"login shell -i -l -c":    {[]string{"/bin/zsh", "-i", "-l", "-c", "hx ."}, "zsh", "hx"},
		"shell -c empty":          {[]string{"bash", "-c", "   "}, "bash", "bash"},
		"interactive shell":       {[]string{"bash"}, "bash", "bash"},
		"empty argv":              {nil, "sleep", "sleep"},
		"unreadable argv":         {[]string{""}, "less", "less"},
		"extension only":          {[]string{"python3", ".py"}, "python3", "python3"},
		"runner shows as itself":  {[]string{"uv", "run", "tool"}, "uv", "uv"},
		"interpreter path":        {[]string{"/usr/bin/python3", "-m", "http.server"}, "python3", "http.server"},
		"assignment not for node": {[]string{"node", "A=1"}, "node", "A=1"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resolve(tc.argv, tc.short); got != tc.want {
				t.Errorf("resolve(%q, %q) = %q, want %q", tc.argv, tc.short, got, tc.want)
			}
		})
	}
}

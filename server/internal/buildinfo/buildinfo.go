// Package buildinfo holds metadata injected by release builds.
package buildinfo

const name = "atc"

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func Current() Info {
	return Info{
		Name:    name,
		Version: version,
		Commit:  commit,
		Date:    date,
	}
}

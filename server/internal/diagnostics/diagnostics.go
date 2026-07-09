package diagnostics

import "github.com/jeremytondo/atelier-code/internal/buildinfo"

type Health struct {
	Status string `json:"status"`
}

type Version struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Diagnostics struct {
	name    string
	version string
	commit  string
}

func DefaultDiagnostics() Diagnostics {
	info := buildinfo.Current()
	return Diagnostics{
		name:    info.Name,
		version: info.Version,
		commit:  info.Commit,
	}
}

func (d Diagnostics) Health() Health {
	return Health{Status: "ok"}
}

func (d Diagnostics) Version() Version {
	return Version{
		Name:    d.name,
		Version: d.version,
		Commit:  d.commit,
	}
}

package remote

// The discovery contract of guided setup (ATC-325): what `atc __remote
// inspect` prints on the target so the initiating CLI can decide, and
// show, what the machine needs. Facts only — the remote never decides
// compatibility, the local side compares protocols. Decoded leniently
// (unknown fields ignored) because a candidate build under inspection
// may be newer than the reader.

// Inspection is the one JSON object `atc __remote inspect` prints.
type Inspection struct {
	Executable Executable `json:"executable"`
	Server     Server     `json:"server"`
	Tailnet    Tailnet    `json:"tailnet"`
}

// Executable is the identity of the atc that ran the inspection.
type Executable struct {
	// Path is the resolved location (symlinks followed), where an update
	// would land.
	Path string `json:"path"`
	// Version is the build identity; Channel classifies it (stable, dev,
	// or "" for an unpublished build); Protocol is the contract it speaks.
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Protocol int    `json:"protocol"`
	// Writable reports whether the running user can replace the file
	// (its directory accepts a new entry). Managed reports a location a
	// package manager owns, which guided setup never overwrites.
	Writable bool `json:"writable"`
	Managed  bool `json:"managed"`
}

// Server describes the supervised server as the inspecting executable
// sees it: the executable its unit runs, and what answered the probe.
type Server struct {
	// Executable is the path the installed unit execs; "" when no unit is
	// installed.
	Executable string `json:"executable"`
	// Supervised reports whether the supervisor has the unit active.
	Supervised bool `json:"supervised"`
	// Responding is any HTTP answer; Healthy an authenticated success on
	// the inspecting executable's protocol. Version and Protocol are what
	// answered: both empty means something that is not ATC answered.
	Responding bool   `json:"responding"`
	Healthy    bool   `json:"healthy"`
	Version    string `json:"version"`
	Protocol   int    `json:"protocol"`
}

// ATC reports whether what answered identified itself as an ATC server
// at all (any release, any protocol).
func (s Server) ATC() bool { return s.Responding && (s.Version != "" || s.Protocol != 0) }

// Tailnet is the state of ATC's own tailnet exposure setting and of the
// Tailscale node it depends on.
type Tailnet struct {
	// Configured is config.toml's tailscale setting. Launch is the
	// running launch's --tailscale flag: nil when none was supplied or no
	// launch is running.
	Configured bool  `json:"configured"`
	Launch     *bool `json:"launch"`
	// Problem says why Tailscale cannot serve ATC — not installed, not
	// running, logged out — with the remedy; "" when the node is up.
	Problem string `json:"problem"`
}

// Exposed reports whether the running launch, or a fresh one, would
// expose the API on the tailnet: the launch flag when supplied,
// configuration otherwise.
func (t Tailnet) Exposed() bool {
	if t.Launch != nil {
		return *t.Launch
	}
	return t.Configured
}

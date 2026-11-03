package sandbox

// Presets are starting points, not promises. Each one is a declarative template
// an operator can copy, change or ignore; none of them is registered unless the
// operator asks for it, because a preset that silently becomes the default is how
// a control plane ends up running whatever binary happens to be installed.
//
// The vocabulary is the one in docs/sandbox-landscape.md: nsjail and isolate are
// layer ④, nerdctl is layer ③.
func PresetSpecs() []DeclarativeSpec {
	return []DeclarativeSpec{
		{
			Name:      "nsjail",
			Layer:     string(LayerProcess),
			Isolation: IsolationNamespace,
			Binary:    "/usr/bin/nsjail",
			License:   "Apache-2.0",
			DocsURL:   "https://github.com/google/nsjail",
			// nsjail clones a network namespace by default (--disable_clone_newnet
			// is what opts out), and this argv does not opt out.
			Network:   []string{NetworkNone},
			ProbeArgs: [][]string{{"--help"}},
			ExecArgs: [][]string{
				{"--quiet", "--mode", "o", "--user", "65534", "--group", "65534",
					"--chroot", "/", "--cwd", "{workdir}", "--time_limit", "{timeout_s}",
					"--rlimit_as", "{memory_mb}", "--", "{command}"},
				{"{arg}"},
			},
		},
		{
			Name:      "isolate",
			Layer:     string(LayerProcess),
			Isolation: IsolationNamespace,
			Binary:    "/usr/local/bin/isolate",
			License:   "GPL-2.0",
			DocsURL:   "https://github.com/ioi/isolate",
			// isolate only joins the host network when --share-net is passed, which
			// this argv does not.
			Network:   []string{NetworkNone},
			ProbeArgs: [][]string{{"--version"}},
			ExecArgs: [][]string{
				{"--silent", "--run", "--processes={pids}", "--time={timeout_s}",
					"--mem={memory_mb}", "--dir={workdir}", "--", "{command}"},
				{"{arg}"},
			},
			DestroyArgs: [][]string{{"--cleanup"}},
		},
		{
			Name:      "systemd-run",
			Layer:     string(LayerProcess),
			Isolation: IsolationNamespace,
			Binary:    "/usr/bin/systemd-run",
			License:   "LGPL-2.1-or-later",
			DocsURL:   "https://www.freedesktop.org/software/systemd/man/systemd-run.html",
			// PrivateNetwork=yes was added rather than declaring "platform": the
			// previous argv inherited the host network while the isolation level
			// (namespace) implied it did not.
			Network:   []string{NetworkNone},
			ProbeArgs: [][]string{{"--version"}},
			ExecArgs: [][]string{
				{"--quiet", "--pipe", "--wait", "--collect",
					"--property=PrivateTmp=yes", "--property=PrivateNetwork=yes",
					"--property=NoNewPrivileges=yes",
					"--property=ProtectSystem=strict", "--property=ProtectHome=yes",
					"--property=DynamicUser=yes", "--", "{command}"},
				{"{arg}"},
			},
		},
		{
			Name:      "nerdctl",
			Layer:     string(LayerContainer),
			Isolation: IsolationContainer,
			Binary:    "/usr/local/bin/nerdctl",
			License:   "Apache-2.0",
			DocsURL:   "https://github.com/containerd/nerdctl",
			// --network none is hard coded in CreateArgs: no profile can widen it.
			Network:   []string{NetworkNone},
			ProbeArgs: [][]string{{"version", "--format", "{{.Server.Version}}"}},
			CreateArgs: [][]string{{
				"run", "-d", "--rm", "--name", "agenthub-{lease_id}",
				"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev",
				"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
				"-u", "65534:65534", "--pids-limit", "{pids}", "--memory", "{memory_mb}m",
				"-w", "/work", "{image}", "sleep", "infinity",
			}},
			ExecArgs: [][]string{
				{"exec", "-i", "agenthub-{lease_id}", "{command}"},
				{"{arg}"},
			},
			DestroyArgs: [][]string{{"rm", "-f", "agenthub-{lease_id}"}},
		},
	}
}

// PresetByName returns one preset definition.
func PresetByName(name string) (DeclarativeSpec, bool) {
	for _, spec := range PresetSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return DeclarativeSpec{}, false
}

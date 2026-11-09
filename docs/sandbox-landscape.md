# Open-source sandbox landscape

Why this document exists: AgentHub used to call itself a control plane while
leaving every execution to an external process it never constrained. The
sandbox work replaces that with a provider catalogue, and a catalogue is only
useful if the operator can tell what each entry actually buys.

The comparison is organised by **isolation layer**, because that is the only
axis that survives marketing: two products in the same layer differ in
packaging, not in what a compromise can reach.

## 1. Layers

| # | Layer | Representative projects | Boundary enforced by | Cold start | Kernel attack surface | Toolchain compatibility | Licence | How it plugs into AgentHub | Built in |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ① | microVM | Firecracker, Cloud Hypervisor, libkrun, crosvm, Kata Containers | hardware virtualisation + guest kernel | sub-second to seconds (Firecracker ≈125 ms) | minimal — the host kernel is not shared | complete (a real Linux kernel) | Apache-2.0 | Kata through `docker/podman --runtime=kata`; Firecracker-class platforms through the remote node protocol | preset `kata` |
| ② | user-space kernel | gVisor (runsc + Sentry + Gofer) | Sentry intercepts syscalls in user space | ≈100 ms | greatly reduced | good (a few syscalls unimplemented) | Apache-2.0 | `docker --runtime=runsc`, i.e. the docker provider with a runtime argument | preset `gvisor` |
| ③ | container / rootless | Docker, Podman, containerd, runc | namespaces + cgroups + capabilities + seccomp/LSM | <1 s | shared host kernel (weakest of the "real" options) | complete | Apache-2.0 | first-class provider | **`docker` (instance default)**, `podman` |
| ④ | process sandbox | bubblewrap, nsjail, isolate, systemd-run | user/mount namespaces + seccomp + rlimits | milliseconds | shared kernel, narrowed by seccomp | complete, no image concept | LGPL-2.0+ / Apache-2.0 | `bwrap` is a provider; nsjail/isolate/systemd-run are declarative templates | `bwrap`, `local` (restricted, no extra binary) |
| ⑤ | WebAssembly | Wasmtime, WasmEdge, Wasmer | bytecode validation + capability imports | milliseconds | minimal | poor — the toolchain must be compiled to wasm | Apache-2.0 | declarative template, not a primary path | no |
| ⑥ | language runtime | Pyodide, Deno/Node permission model | interpreter-level capabilities | milliseconds | implementation dependent | poor — one language only | mixed | not adopted: it is not an OS-level execution boundary | no |
| — | platform | E2B, microsandbox, Daytona, CubeSandbox, OpenHands runtime | composes the primitives above behind an SDK | 80 ms to seconds | inherited from the substrate | good (SDK) | mostly Apache-2.0 | remote node protocol, or a declarative HTTP template | presets, **no SDK** |

## 2. What each layer actually buys

**① / ② — isolating a hostile agent from the host kernel.** A microVM or a
user-space kernel is the only choice when the code being run is assumed
adversarial rather than merely buggy. The price is operational: images,
runtime installation, and (for microVMs) a host that can do KVM. Neither is a
sensible default for a single-binary control plane, so both are opt-in.

**③ — the default.** Docker is the only layer an operator is likely to have
already. `--network=none --read-only --cap-drop=ALL --security-opt
no-new-privileges --pids-limit --memory --cpus -u 65534` removes most of the
blast radius a prompt-injected command needs, and the defaults are one flag
away from a runtime upgrade (`--runtime=runsc` / `kata`) once one is installed.
The honest limit: a container escape reaches the host kernel, which is why the
descriptor reports `isolation: container` rather than "sandboxed".

**④ — the cheap local option.** `bwrap` starts in milliseconds and needs no
image, which is what makes it the right answer for a developer laptop or a CI
box without a container runtime. The honest limit: without a seccomp profile it
is a namespace boundary, not a syscall boundary, and AgentHub labels it as
such. nsjail and isolate add seccomp; both are reachable as declarative
templates so they do not need a Go release to become available.

**Platforms — do not embed the SDK.** E2B, microsandbox, Daytona, CubeSandbox
and OpenHands each own a lifecycle vocabulary (`sandbox`, `machine`,
`workspace`, `session`) and a client library. Baking one in would make a
vendor's SDK a production dependency of AgentHub and would pick a winner on the
operator's behalf. The remote node protocol keeps them at arm's length: a thin
adapter translates create/exec/attach/destroy/status, and the credential is
referenced by environment variable name, never stored.

## 3. Selection rules

1. **Default to ③.** `AGENTHUB_SANDBOX_PROVIDER=docker`; unavailable means
   refuse (`sandbox_unsupported`), never fall back to something weaker.
2. **Upgrade to ② / ① when the code is untrusted.** `--runtime=runsc` is a
   one-word change; Kata the same. Full microVM fleets belong behind the remote
   protocol.
3. **Drop to ④ when there is no container runtime.** `bwrap`, or `local` with
   its (documented) lack of isolation, for a laptop or a constrained CI host.
4. **Use `mock` only to stay offline.** Tests, `make web-dev` and demos set it
   explicitly; it executes nothing and says so in every receipt.
5. **Never let the provider choice be inferred.** A resource that names a
   provider it cannot get is rejected at registration time, not downgraded at
   run time.

## 4. Declared capabilities

`Describe()` is the contract the manager enforces before a lease exists: `Network`
lists the modes a provider can actually apply, `Requires` the binaries a probe has
to find, `EnvKeys` the environment variables a shim needs, and `Source` whether it
is built in, declarative, a remote node or a platform shim. `delegated` is the
honest value for a layer that hands the profile to somebody else's enforcement
point: it says where the boundary lives, not that none exists.

Per backend that means: `docker` advertises `none|bridge|platform` and maps
`platform` onto the daemon's own default network (never a literal `bridge`);
`bwrap` advertises `none|platform` only, because `--share-net` is the host's
network namespace and there is no NAT interface to offer; `local` advertises
`platform` and nothing else; remote nodes, platform shims and templates that
declare no network all advertise `delegated`.

`Manager.Acquire` refuses a profile whose mode the provider never declared —
`sandbox_capability_unavailable`, with the provider never called. Before that
rule existed the declaration was decoration, and asking bubblewrap for `bridge`
silently returned the host's network.

## 5. Where AgentHub sits

AgentHub is the **control plane**: it decides who may execute, where, with what
limits, for how long, and it records what happened. It is deliberately not a
sandbox implementation. The provider catalogue is the seam: adding a layer
means registering a provider or writing a template, not changing the hub, the
adapters or the console.

See `requirements-sandbox.md` for the API and table contracts, and
`internal/sandbox/presets.go` for the templates referenced above.

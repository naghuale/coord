# Linux seccomp profile

The profile is allowlist-based with default `ERRNO(EPERM)`. Explicit dangerous
syscalls are documented even though default-deny already rejects them. The
launcher uses libseccomp Python bindings to load the filter immediately before
`exec`.

The provided allowlist is a starting profile for a Python agent, not a universal
profile. Kernel, architecture, libc and workload differences can require
changes. Unknown syscall names fail closed. Audit the profile on every supported
runner architecture and combine it with namespaces, filesystem policy, cgroups
and process-tree supervision.

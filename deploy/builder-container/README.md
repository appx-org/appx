# Builder-container deploy assets (Stage 3)

appx supervises the agent-server **outer builder container** in container mode
(`APPX_AGENT_CONTAINER=true`). The supervisor (`internal/containerruntime`)
builds the `docker run` flag set and references the tailored seccomp profile in
this directory by absolute path.

## `seccomp-builder.json`

The **security boundary**. This is a verbatim copy of agent-server's
`container/seccomp-builder.json` — podman's stock profile with the
`CAP_SYS_ADMIN` gate removed from **only** `sethostname`, `setdomainname`,
`setns` (the namespace-setup syscalls nested rootless podman needs). It is
**strictly tighter than `seccomp=unconfined`**; the genuinely dangerous gated
syscalls (`bpf`, `perf_event_open`, `quotactl`, `fanotify_init`,
`lookup_dcookie`) stay denied. See agent-server's
`container/SPIKE-FINDINGS.md` (Stage 0, task T2) and `container/gen-seccomp.sh`
for provenance.

### Why a copy lives here (drift note)

The SPIKE-FINDINGS recommend shipping the profile alongside the deploy scripts
and referencing it by absolute path. appx needs the file on the host at
`docker run` time (`--security-opt seccomp=<path>`), so it cannot live only
inside the image.

**This is a duplicate of agent-server's canonical copy.** If `gen-seccomp.sh`
changes there (e.g. the base podman version bumps), re-copy it here. A future
cleanup could publish the profile as an image artefact appx extracts, but for
Stage 3 the copy + this note is the documented trade-off.

`deploy/tools-install.sh` installs this file to `/etc/appx/seccomp-builder.json`
(0644) and `bootstrap.sh` points `APPX_AGENT_SECCOMP` at it.

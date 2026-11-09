# Containers Inside the Sandbox

Rootless podman runs inside a bwrap sandbox with a small wrapper script and two config files. That covers the common case of starting a database or a cache next to the code an agent is working on. devsandbox does not ship this yet; this page is the manual recipe, its limits, and the support proposed to replace it.

The [Docker tool](tools.md#docker) is a different thing: it gives read-only access to the host's Docker daemon and cannot start containers. Podman inside the sandbox starts containers that live entirely inside it.

## Status

| | |
| --- | --- |
| Backend | `bwrap` only. The `docker` and `krun` backends are untested. |
| Tested with | podman 6.1.4, crun, util-linux 2.42, Linux 7.2 |
| Works | pulling images (through the proxy in proxy mode), `run`, `run -d`, `ps`, `exec`, `logs`, `rm`, postgres with `--userns=keep-id` |
| Does not work | images that `chown` to or switch to another user without extra flags, `-p` port publishing, container networks and service-name DNS, podman's `--memory`/`--cpus` limits |

## Requirements

- podman, crun and util-linux 2.38 or newer (for `unshare --map-user`) installed on the host, so they are visible inside the sandbox.
- Kernel 5.11 or newer, for overlay storage in a user namespace. No `/dev/fuse` or `fuse-overlayfs` is needed.
- Unprivileged user namespaces, which the bwrap backend already requires.

Check that the sandbox sees the tools:

```bash
devsandbox sh -c 'command -v podman crun nsenter && unshare --version'
```

## Setup

All of the following happens inside the sandbox. The sandbox home persists between sessions of the same project, so you do this once per project.

### 1. Save the Namespace Wrapper

Save this as `~/bin/podman-ns` in the sandbox and make it executable (`chmod +x ~/bin/podman-ns`):

```sh
#!/bin/sh
# podman-ns: run a command inside a long-lived namespace where rootless
# podman works under devsandbox's bwrap backend.
#   podman-ns podman run --rm docker.io/library/alpine echo hi
set -eu

state="${XDG_RUNTIME_DIR:-/tmp}/podman-ns"
mkdir -p "$state"

exec 9>"$state/lock"
flock 9

pid=$(cat "$state/holder.pid" 2>/dev/null || true)
if [ -z "$pid" ] || [ "$(cat "/proc/$pid/comm" 2>/dev/null)" != sleep ]; then
	rm -f "$state/holder.pid"
	# The sandbox root is a writable tmpfs, so the mount point can be created.
	mkdir -p /sys/fs/cgroup
	# Root in a nested user namespace may mount cgroup2 in its own cgroup
	# namespace. A second user namespace then maps back to your uid, so
	# podman runs rootless rather than as a root that cannot mount devpts.
	setsid unshare --user --map-root-user --mount --cgroup sh -c '
		mount -t cgroup2 cgroup2 /sys/fs/cgroup
		echo $$ > "$1"
		exec unshare --user --map-user="$2" --map-group="$3" sleep infinity
	' sh "$state/holder.pid" "$(id -u)" "$(id -g)" </dev/null >/dev/null 2>&1 9>&- &
	i=0
	until pid=$(cat "$state/holder.pid" 2>/dev/null) &&
		[ "$(cat "/proc/$pid/comm" 2>/dev/null)" = sleep ]; do
		i=$((i + 1))
		[ "$i" -lt 50 ] || { echo "podman-ns: holder namespace did not start" >&2; exit 1; }
		sleep 0.1
	done
fi

exec 9>&-
exec nsenter -t "$pid" --user --mount --cgroup --preserve-credentials -- "$@"
```

The first call starts a holder process that owns the namespaces; every later call joins it, so a container started with `-d` is still there for the next `podman ps`. The holder dies with the sandbox session. The lock keeps two concurrent first calls from starting two holders.

### 2. Configure Podman

`~/.config/containers/containers.conf`:

```toml
[containers]
# No writable cgroup tree inside the sandbox; devsandbox's own limits still apply.
cgroups = "disabled"
# Share the sandbox network: proxy mode and egress rules apply, nothing reaches the host.
netns = "host"

[engine]
cgroup_manager = "cgroupfs"
events_logger = "file"
```

`~/.config/containers/storage.conf`:

```toml
[storage]
driver = "overlay"

[storage.options]
# Only one uid is mapped, so layers owned by other uids keep your uid instead.
ignore_chown_errors = "true"
```

Images land in `~/.local/share/containers/storage` in the sandbox home. They persist with the sandbox and are separate from the host's podman store, so each project pulls its own copy. `devsandbox sandboxes prune` removes them with the rest of the sandbox.

### 3. Run a Container

```bash
~/bin/podman-ns podman run --rm docker.io/library/alpine:3.20 echo hello
```

Podman warns `Using rootless single mapping into the namespace` and prints `hwloc/linux: failed to find sysfs cpu topology directory` on every call. Both are expected here; see [Limitations](#limitations).

To have an agent use it, say so in the project's `CLAUDE.md` or `AGENTS.md`, for example: "Run podman as `~/bin/podman-ns podman ...`; plain `podman` fails in this sandbox."

## Running a Database

The container gets exactly one uid, so an image whose entrypoint `chown`s its data directory and then drops to a service user fails with `chown: ...: Invalid argument`. Most database images also accept running as an arbitrary user, which avoids both steps. Run them as yourself with a data directory that user can write:

```bash
~/bin/podman-ns podman run -d --name pg \
  --userns=keep-id --user "$(id -u):$(id -g)" \
  -e POSTGRES_PASSWORD=dev -e PGDATA=/tmp/pgdata \
  docker.io/library/postgres:17-alpine

~/bin/podman-ns podman exec pg psql -U postgres -h 127.0.0.1 -c 'select 1'
```

Postgres listens on `127.0.0.1:5432` in the sandbox, so code running anywhere in the sandbox connects to it directly. `PGDATA=/tmp/pgdata` keeps the data inside the container and loses it on `podman rm`; mount a directory with `-v` to keep it.

## Networking

`netns = "host"` puts containers in the sandbox's own network namespace rather than giving each one its own:

- A container's listening ports appear on the sandbox's loopback. Nothing is published to the host; use [port forwarding](sandboxing.md#port-forwarding) to reach one from outside.
- In [proxy mode](proxy.md) the egress lockdown applies to containers exactly as to everything else in the sandbox, so a container has no direct path out and its traffic goes through the proxy. Image pulls go through the proxy too, and with `default_action = "block"` the registry hosts need allow rules.
- Podman copies `HTTP_PROXY` and friends into containers by default, but not the MITM CA certificate. HTTPS from inside a container under a MITM proxy fails unless the image is given the CA. This follows from how podman and the proxy work; it has not been tested.
- `-p` is ignored, there is no container-to-container DNS, and two containers cannot listen on the same port. Compose files that rely on service names or published ports need adjusting.

## Limitations

- **One uid per container.** The bwrap sandbox maps a single uid and runs with `no_new_privs`, so the setuid `newuidmap` helper that normally gives rootless podman a range of subordinate ids cannot work. The `Using rootless single mapping` warning says so. Images that need several distinct uids at runtime cannot run.
- **No podman resource limits.** Podman has no writable cgroup tree, so `--memory`, `--cpus` and `--pids-limit` are not applied. devsandbox's own [resource limits](sandboxing.md#resource-limits) still cover the whole sandbox, containers included.
- **No `/sys`.** The sandbox has no sysfs, and the kernel refuses a fresh one inside a nested namespace. Containers get no usable `/sys`, and anything that reads hardware topology from it (the `hwloc` message) degrades.
- **bwrap only.** The Docker backend already runs the sandbox in a container, and krun in a microVM; neither has been tried.

## Troubleshooting

| Error | Cause | Fix |
| --- | --- | --- |
| `Error determining cgroups mode` | Plain `podman` without the wrapper: there is no `/sys/fs/cgroup` in the sandbox. | Run it through `podman-ns`. |
| `Cgroups v1 not supported` | Something other than cgroup2 is mounted at `/sys/fs/cgroup`, usually a tmpfs created by hand. Podman 6 needs cgroup2. | Run it through `podman-ns`, which mounts cgroup2. |
| `VFS: Mount too revealing` | A fresh sysfs mount inside a nested namespace, which crun attempts for a container with its own network namespace. The kernel allows one only when a full sysfs is already visible, and the sandbox has none. | Keep `netns = "host"` as above, which makes crun skip the sysfs mount. |
| `crun: mount devpts to dev/pts: Invalid argument` | Podman ran as root inside a user namespace, so it chose rootful mode and asked for a gid that is not mapped. | Run it through `podman-ns` as your own user, not under `unshare --map-root-user`. |
| `chown: ...: Invalid argument` inside a container | The image changes ownership to a uid that does not exist in the single mapping. | `--userns=keep-id --user "$(id -u):$(id -g)"`, see [Running a Database](#running-a-database). |
| `podman-ns: holder namespace did not start` | `unshare` or the cgroup2 mount failed. | Run the `unshare` line from the script by hand to see the error. util-linux older than 2.38 has no `--map-user`. |

## How It Works

Podman 6 refuses to start unless `/sys/fs/cgroup` is a cgroup2 filesystem, and the sandbox has no `/sys`. A process that is root in a new user namespace may mount cgroup2 inside a new cgroup namespace, because cgroup2 is not one of the filesystems (proc and sysfs) the kernel restricts to an already-visible instance. The mount shows only the sandbox's own cgroup subtree.

Podman must then run as your uid, not as that namespace's root. As root it assumes it is rootful and asks crun to mount `devpts` with `gid=5`, which is not mapped. So the wrapper nests a second user namespace mapping your uid back, and podman takes its normal rootless path with a single-id mapping.

Every step uses kernel features any process in the sandbox can already reach: nested user, mount and cgroup namespaces, and mounts inside them. The recipe grants nothing the sandbox did not already have, and the sandbox's network namespace, egress rules and cgroup still contain everything podman starts.

## Proposed devsandbox Support

None of this is implemented. These are the changes that would replace the manual recipe, roughly in order of effort.

1. **A `[tools.podman]` tool, opt-in, bwrap only.** Add a `/sys/fs/cgroup` mount point to the sandbox, put the namespace wrapper on `PATH` as `podman`, and force the required settings through `CONTAINERS_CONF_OVERRIDE` so a user's own `containers.conf` still applies. On the docker and krun backends it would refuse with a clear message rather than install a wrapper that cannot work. This removes every manual step on this page without changing any isolation property.
2. **`/dev/net/tun` in the sandbox, as an opt-in.** With a tun device podman can give each container its own network namespace through pasta, which brings back `-p`, container networks and service-name DNS, and with them compose files and testcontainers. A tun device only creates interfaces in network namespaces the caller owns, so it cannot touch the sandbox's own namespace or its egress rules. Unverified: the sandbox has no tun device to test with.
3. **The MITM CA inside containers.** Mount the session's CA into containers through `containers.conf` `volumes` and set the usual CA variables, so HTTPS from a container works under a MITM proxy.
4. **Subordinate uid ranges.** Build the sandbox's outer user namespace with the host's `newuidmap`/`newgidmap` and the user's `/etc/subuid` range, and hand it to bwrap with `--userns`/`--userns2`, so containers get real uid ranges and unmodified images work. This rebuilds the user namespace chain that keeps the sandbox from altering its own egress rules, so it needs its own design and review.

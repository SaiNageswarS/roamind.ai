#!/usr/bin/env bash
# Serves Jupyter (JupyterLab at /lab, Notebook 7 at /tree) on loopback
# 127.0.0.1:8890, for Caddy's notebook.<tailnet>.ts.net. Started by
# process-compose; install with task notebook.
#
# Notebooks run any code, so the server runs in a bubblewrap sandbox:
#   - it sees the system read-only, the environment (notebook/.venv and uv's
#     managed Pythons) read-only, and the GPU devices;
#   - the home directory, the repo and data/ are hidden, except
#     data/notebook, its home, which holds the notebooks in notebooks/;
#   - it gets no environment from .env (--clearenv), so no tokens;
#   - the network is shared, for Caddy to reach it and for downloads.
# Packages are added to notebook/pyproject.toml, not with %pip: the
# environment is read-only.
#
# Jupyter asks for no token: Caddy lets in only ROAMIND_DASHBOARD_USERS.
# JAX allocates GPU memory as it needs it, since vLLM holds most of the GPU.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
data="${ROAMIND_DATA_DIR:-$ROOT/data}"
mkdir -p "$data/notebook/notebooks"
home="$(cd "$data/notebook" && pwd)"
venv="$ROOT/notebook/.venv"
[[ -x "$venv/bin/jupyter" ]] || {
  echo "run-notebook: $venv not found; run task notebook" >&2
  exit 1
}
# uv's managed Pythons: the venv's python links to one through a version
# alias (cpython-3.12-...), so both must be visible.
pythons="$(dirname "$(dirname "$(dirname "$(readlink "$venv/bin/python")")")")"

args=(
  --die-with-parent --new-session
  --unshare-all --share-net
  --clearenv
  --ro-bind /usr /usr
  --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib64 /lib64 --symlink usr/sbin /sbin
  --ro-bind /etc /etc
  --ro-bind /sys /sys
  --proc /proc --dev /dev --tmpfs /tmp
  --ro-bind "$pythons" "$pythons"
  --ro-bind "$ROOT/notebook" "$ROOT/notebook"
  --bind "$home" "$home"
  --setenv HOME "$home"
  --setenv PATH "$venv/bin:/usr/bin"
  --setenv LANG C.UTF-8
  --setenv XLA_PYTHON_CLIENT_PREALLOCATE false
  --chdir "$home/notebooks"
)
# /etc/resolv.conf links into systemd-resolved's runtime directory.
[[ -d /run/systemd/resolve ]] && args+=(--ro-bind /run/systemd/resolve /run/systemd/resolve)
for dev in /dev/nvidia*; do
  [[ -e "$dev" ]] && args+=(--dev-bind "$dev" "$dev")
done

# Started as Notebook 7, which opens at /tree; JupyterLab loads with it.
exec bwrap "${args[@]}" -- "$venv/bin/jupyter" notebook \
  --no-browser --ip=127.0.0.1 --port=8890 --ServerApp.port_retries=0 \
  --ServerApp.root_dir="$home/notebooks" \
  --IdentityProvider.token= --ServerApp.allow_remote_access=True

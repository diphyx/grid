#!/usr/bin/env bash
# Fails unless the tag is of the generation in the code: its major equals wire.MAJOR
# (pkg/wire/major.go), and go.mod's module ends in /v<major> from 2 on (docs/versions.md).
#
#   .github/scripts/major.sh v2.0.1
set -euo pipefail

tag="${1:?usage: major.sh <tag>}"
major=$(sed -n 's/^[[:space:]]*MAJOR[[:space:]]*= "\([0-9]*\)".*/\1/p' pkg/wire/major.go)
module=$(sed -n 's/^module //p' go.mod)

want="${tag#v}"
want="${want%%.*}"
if [[ "$want" != "$major" ]]; then
    echo "$tag is grid $want, the code is grid $major: set MAJOR in pkg/wire/major.go" >&2
    exit 1
fi
if [[ "$major" -ge 2 && "$module" != */v"$major" ]]; then
    echo "go.mod: module $module, want it to end in /v$major" >&2
    exit 1
fi

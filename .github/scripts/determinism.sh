#!/usr/bin/env bash
# Regenerates every example under conditions that must not change a byte of
# what servo writes, and fails if any committed generated file changes.
#
# servo's output is meant to be a function of the code alone, but it is
# assembled from things that vary by machine: absolute file paths, where the
# module cache lives, whether dependencies are vendored, whether a go.work
# is in scope, and the operating system's path separator and line endings.
# The committed files are the reference, so each scenario regenerates them
# and asks git whether anything moved:
#
#   in place          the checkout as it is
#   moved checkout    a copy of the repository at another absolute path
#   moved modcache    every module downloaded afresh into another GOMODCACHE
#   vendored          each example after `go mod vendor`
#   workspace         every example module inside one go.work
#
# go.yml runs this on Linux, macOS and Windows. Locally, run it from a clean
# checkout: it refuses a dirty tree, because it resets the generated files
# between scenarios.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
	echo "determinism: the working tree is not clean; commit or stash first" >&2
	git status --short >&2
	exit 2
fi

work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT

# A path the go command can read. Under Git Bash on Windows, mktemp hands
# out /tmp/... paths that only the shell understands; cygpath translates
# them. Everywhere else a path is already native.
native() {
	if command -v cygpath > /dev/null 2>&1; then
		cygpath -m "$1"
	else
		printf '%s\n' "$1"
	fi
}

# One binary for every scenario, so they differ only in their surroundings.
servo="$work/bin/servo$(go env GOEXE)"
go build -o "$servo" ./cmd/servo

# Every example with committed generated files; variants has a second build
# configuration and so a second generated file. diagnostics is deliberately
# unresolvable and has none.
examples=(basic scoped mocking http variants tutorial)

# generate <checkout>: every example, carrying on past a failure so the
# comparison still shows everything that changed. set -e does not apply
# inside a function whose status is tested, hence the explicit status.
generate() {
	local tree=$1 dir status=0
	for dir in "${examples[@]}"; do
		(cd "$tree" && "$servo" generate --dir "examples/$dir") || status=1
	done
	(cd "$tree" && "$servo" generate --dir examples/variants --tags=prod) || status=1
	return "$status"
}

failed=()

# check <scenario> <checkout> [NAME=value...]: generate with the given
# variables in the environment, then treat anything git sees as changed or
# new as a difference. The tree is reset afterwards, so the next scenario
# starts from the committed files again.
check() {
	local name=$1 tree=$2
	shift 2
	echo "--- $name"
	if ! (
		for assignment in "$@"; do
			export "${assignment?}"
		done
		generate "$tree"
	); then
		echo "FAIL  $name: servo generate failed"
		failed+=("$name")
	fi
	if [ -z "$(git -C "$tree" status --porcelain --untracked-files=all)" ]; then
		echo "ok    $name"
		return
	fi
	echo "FAIL  $name: the regenerated files differ from the committed ones"
	git -C "$tree" status --short
	git -C "$tree" --no-pager diff | head -n 200
	[[ " ${failed[*]-} " == *" $name "* ]] || failed+=("$name")
	git -C "$tree" checkout --quiet -- .
	git -C "$tree" clean -fdq
}

check "in place" "$root"

mkdir -p "$work/moved"
cp -R "$root" "$work/moved/servo"
check "moved checkout" "$work/moved/servo"

# -modcacherw so the trap can delete the module cache afterwards.
check "moved modcache" "$root" GOMODCACHE="$(native "$work/modcache")" GOFLAGS=-modcacherw

# vendor/ is in .gitignore, so only the generated files can show up as
# changed; the vendor directories are removed again afterwards.
vendored=true
for dir in "${examples[@]}"; do
	(cd "examples/$dir" && go mod vendor) || vendored=false
done
if [ "$vendored" = true ]; then
	check "vendored" "$root"
else
	echo "FAIL  vendored: go mod vendor failed"
	failed+=("vendored")
fi
for dir in "${examples[@]}"; do
	rm -rf "examples/$dir/vendor"
done

# GOWORK names a workspace file outside the checkout, so the checkout stays
# clean. It lists every example module, the way a monorepo's go.work lists
# its services, and its go line is the running toolchain's, which is at
# least every module's.
gover=$(go env GOVERSION)
gover=${gover#go}
gover=${gover%% *}
{
	printf 'go %s\n\nuse (\n' "$gover"
	for dir in "${examples[@]}" diagnostics; do
		printf '\t%s\n' "$(native "$root/examples/$dir")"
	done
	printf ')\n'
} > "$work/go.work"
check "workspace" "$root" GOWORK="$(native "$work/go.work")"

echo
if [ ${#failed[@]} -gt 0 ]; then
	echo "determinism: ${#failed[@]} scenario(s) changed servo's output: ${failed[*]}"
	exit 1
fi
echo "determinism: every scenario reproduced the committed files byte for byte"

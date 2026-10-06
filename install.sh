#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
bin_dir=${HOME:?HOME must be set}/.local/bin
target=$bin_dir/hyprtheme

mkdir -p "$bin_dir"
if [ -d "$target" ]; then
	printf 'Cannot install: %s is a directory\n' "$target" >&2
	exit 1
fi
tmp_bin=$(mktemp "$bin_dir/.hyprtheme.XXXXXX")
cleanup() {
	if [ -n "${tmp_bin:-}" ]; then
		rm -f -- "$tmp_bin"
	fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

cd "$script_dir"
go build -buildvcs=false -o "$tmp_bin" ./cmd/hyprtheme
chmod 755 "$tmp_bin"
mv -f -- "$tmp_bin" "$target"
tmp_bin=

printf 'Installed %s\n' "$target"

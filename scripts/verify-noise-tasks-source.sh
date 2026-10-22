#!/bin/sh
set -eu

source_dir=${1:-}
expected_ref=${2:-}

if [ -z "$source_dir" ] || [ -z "$expected_ref" ]; then
	echo "usage: verify-noise-tasks-source.sh <source-dir> <expected-commit>" >&2
	exit 2
fi

case "$expected_ref" in
	????????????????????????????????????????)
		case "$expected_ref" in
			*[!0-9a-f]*)
				echo "Tasks source verification: expected commit must be a full lowercase SHA-1" >&2
				exit 2
				;;
		esac
		;;
	*)
		echo "Tasks source verification: expected commit must be a full lowercase SHA-1" >&2
		exit 2
		;;
esac

actual_ref=$(git -C "$source_dir" rev-parse --verify HEAD^{commit} 2>/dev/null) || {
	echo "Tasks source verification: $source_dir is not a Git checkout" >&2
	exit 1
}
if [ "$actual_ref" != "$expected_ref" ]; then
	echo "Tasks source verification: HEAD is $actual_ref, require $expected_ref" >&2
	exit 1
fi

if [ -n "$(git -C "$source_dir" status --porcelain --untracked-files=all)" ]; then
	echo "Tasks source verification: $source_dir has uncommitted or untracked files" >&2
	exit 1
fi

#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --after-verified-deploy <builder> [builder ...]" >&2
  exit 64
}

test "$#" -ge 2 || usage
test "$1" = "--after-verified-deploy" || usage
shift

builders=""
for builder in "$@"; do
  case "$builder" in
    ""|[!A-Za-z0-9]*|*[!A-Za-z0-9_.-]*)
      echo "prune-build-cache: invalid builder name: $builder" >&2
      exit 64
      ;;
  esac
  case " $builders " in
    *" $builder "*)
      echo "prune-build-cache: duplicate builder name: $builder" >&2
      exit 64
      ;;
  esac
  builders="$builders $builder"
done

command -v docker >/dev/null 2>&1 || {
  echo "prune-build-cache: docker is not available" >&2
  exit 69
}

# Resolve every target before the first mutation. A typo must not leave a
# partially pruned builder set with ambiguous accounting.
for builder in $builders; do
  if ! docker buildx inspect "$builder" >/dev/null 2>&1; then
    echo "prune-build-cache: builder does not exist or is unavailable: $builder" >&2
    exit 69
  fi
done

filesystem_path=${NOISEDECK_DEPLOYMENT_FILESYSTEM_PATH:-/}
if test ! -d "$filesystem_path"; then
  echo "prune-build-cache: filesystem path is not a directory: $filesystem_path" >&2
  exit 64
fi

echo "Build cache before verified-deployment cleanup:"
docker system df
df -h "$filesystem_path"

for builder in $builders; do
  echo "Pruning all cache from BuildKit builder: $builder"
  docker buildx prune --builder "$builder" --all --force
done

echo "Build cache after verified-deployment cleanup:"
docker system df
df -h "$filesystem_path"

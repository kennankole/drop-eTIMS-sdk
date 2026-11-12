#!/usr/bin/env bash
#
# Cuts per-module releases on push to main.
#
# This is a multi-module monorepo (one go.mod per eTIMS capability, see
# CONTRIBUTING.md). For each module directory, this script:
#
#   1. Finds the module's last "<dir>/vX.Y.Z" tag, if any.
#   2. Looks at commits since that tag which touched files under the
#      module's own path (commits touching only other modules, or
#      repo-level files like CI config or the root README, don't count).
#   3. Derives a semver bump from Conventional Commits subjects in that
#      range: any "!" or "BREAKING CHANGE:" footer -> major; any "feat"
#      -> minor (if no major); any "fix"/"perf" -> patch (if neither).
#      Commits that don't match (docs, chore, test, ci, refactor, ...)
#      don't count on their own.
#   4. If nothing release-worthy touched the module, it's left alone. If
#      the module has no prior tag at all, it gets an initial v0.1.0
#      unconditionally (there's no previous version to bump from).
#   5. Unless --dry-run, creates and pushes the tag, then creates a
#      GitHub Release for it via `gh`.
#
# Usage: .github/scripts/release.sh [--dry-run]
#
# Requires: git (full history + tags fetched), gh (authenticated via
# GH_TOKEN) unless --dry-run.

set -euo pipefail

dry_run=false
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=true
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

# bump_version CURRENT_VERSION BUMP_TYPE
# CURRENT_VERSION has no leading "v" (e.g. "1.2.3"). Prints the bumped
# version, also with no leading "v".
bump_version() {
  local ver=$1 bump=$2
  IFS='.' read -r major minor patch <<<"$ver"
  case "$bump" in
    major) echo "$((major + 1)).0.0" ;;
    minor) echo "$major.$((minor + 1)).0" ;;
    patch) echo "$major.$minor.$((patch + 1))" ;;
  esac
}

# classify_commit SHA
# Prints "major", "minor", "patch", or nothing, based on the commit's
# Conventional Commits subject/footer.
classify_commit() {
  local sha=$1 subject body
  subject=$(git log -1 --format='%s' "$sha")
  body=$(git log -1 --format='%b' "$sha")

  if [[ "$subject" =~ ^[a-zA-Z]+(\([^\)]*\))?!: ]] || grep -q '^BREAKING CHANGE:' <<<"$body"; then
    echo "major"
  elif [[ "$subject" =~ ^feat(\([^\)]*\))?: ]]; then
    echo "minor"
  elif [[ "$subject" =~ ^(fix|perf)(\([^\)]*\))?: ]]; then
    echo "patch"
  fi
}

# highest_bump reads bump words (one per line) from stdin and prints the
# highest-priority one (major > minor > patch), or nothing if none seen.
highest_bump() {
  local has_major=false has_minor=false has_patch=false b
  while read -r b; do
    case "$b" in
      major) has_major=true ;;
      minor) has_minor=true ;;
      patch) has_patch=true ;;
    esac
  done
  if $has_major; then
    echo "major"
  elif $has_minor; then
    echo "minor"
  elif $has_patch; then
    echo "patch"
  fi
}

released_any=false

for gomod in $(find . -name go.mod -not -path './.git/*' | sort); do
  dir=$(dirname "$gomod")
  dir=${dir#./}
  echo "::group::$dir"

  last_tag=$(git tag --list "${dir}/v*" --sort=-v:refname | head -n1)

  if [[ -z "$last_tag" ]]; then
    echo "$dir: no prior tag"
    commits=$(git log --format='%H' -- "$dir")
  else
    echo "$dir: last tag is $last_tag"
    commits=$(git log "${last_tag}..HEAD" --format='%H' -- "$dir")
  fi

  if [[ -z "$commits" ]]; then
    echo "$dir: no commits touching this path since last release, skipping"
    echo "::endgroup::"
    continue
  fi

  if [[ -z "$last_tag" ]]; then
    new_version="0.1.0"
    echo "$dir: cutting initial release v$new_version"
  else
    bump=$(for c in $commits; do classify_commit "$c"; done | highest_bump)
    if [[ -z "$bump" ]]; then
      echo "$dir: commits found but none are feat/fix/perf/breaking, skipping"
      echo "::endgroup::"
      continue
    fi
    current_version=${last_tag#"$dir"/v}
    new_version=$(bump_version "$current_version" "$bump")
    echo "$dir: bump=$bump, $current_version -> $new_version"
  fi

  tag="${dir}/v${new_version}"
  released_any=true

  if $dry_run; then
    echo "$dir: [dry-run] would create and push tag $tag"
    echo "::endgroup::"
    continue
  fi

  git tag -a "$tag" -m "$tag"
  git push origin "$tag"

  notes_args=(--title "$dir v$new_version" --generate-notes)
  if [[ -n "$last_tag" ]]; then
    notes_args+=(--notes-start-tag "$last_tag")
  fi
  gh release create "$tag" "${notes_args[@]}"

  echo "$dir: released $tag"
  echo "::endgroup::"
done

if ! $released_any; then
  echo "No modules had release-worthy changes."
fi

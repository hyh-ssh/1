#!/usr/bin/env bash
# Shared deploy guard: production may only be deployed from the main branch.
#
# Sourced by the deployment scripts once ROOT_DIR points at the repository root:
#
#   # shellcheck source=scripts/lib/deploy-guard.sh
#   . "$ROOT_DIR/scripts/lib/deploy-guard.sh"
#   require_main_branch
#
# The guard exists because both deploy scripts compile the Vue console and the Go
# binary straight out of the local working tree, without consulting git. A dirty
# tree or a feature branch would therefore ship code that is not on main.
#
# Overrides, for emergencies only (both print a warning and are never silent):
#   SKIP_BRANCH_GUARD=1    bypass every check below
#   ALLOW_UNPUSHED_MAIN=1  allow main to be ahead of origin/main

deploy_guard_die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_main_branch() {
  local root=${ROOT_DIR:?ROOT_DIR must be set before calling require_main_branch}

  command -v git >/dev/null 2>&1 || deploy_guard_die "the deploy guard needs git on PATH"

  if [[ -n "${SKIP_BRANCH_GUARD:-}" ]]; then
    printf 'warning: SKIP_BRANCH_GUARD is set; deploying without the main-branch guard\n' >&2
    return 0
  fi

  git -C "$root" rev-parse --git-dir >/dev/null 2>&1 ||
    deploy_guard_die "$root is not a git work tree; production deploys must come from a checkout of main"

  local branch
  branch=$(git -C "$root" symbolic-ref --quiet --short HEAD || true)
  if [[ "$branch" != "main" ]]; then
    deploy_guard_die "refusing to deploy: HEAD is ${branch:-a detached commit}, and production deploys are only allowed from 'main'.
    Commit or stash your work, then deploy from main:
      git -C $root switch main && git -C $root pull --ff-only"
  fi

  local dirty
  dirty=$(git -C "$root" status --porcelain)
  if [[ -n "$dirty" ]]; then
    deploy_guard_die "refusing to deploy: the main checkout has uncommitted changes, and the deploy scripts build from the working tree.
    Commit or stash them first (git -C $root status --short), or deploy from a clean worktree:
      git worktree add /tmp/deploy-main main"
  fi

  local head remote_head
  head=$(git -C "$root" rev-parse HEAD)
  if remote_head=$(git -C "$root" rev-parse --verify --quiet origin/main); then
    if [[ "$head" != "$remote_head" && -z "${ALLOW_UNPUSHED_MAIN:-}" ]]; then
      deploy_guard_die "refusing to deploy: main ($head) is not identical to origin/main ($remote_head).
    Push it first (git -C $root push origin main), or set ALLOW_UNPUSHED_MAIN=1 if you really mean to ship an unpushed main."
    fi
    if [[ "$head" != "$remote_head" ]]; then
      printf 'warning: ALLOW_UNPUSHED_MAIN is set; deploying %s while origin/main is at %s\n' "$head" "$remote_head" >&2
    fi
  else
    printf 'warning: origin/main is unknown here; skipping the pushed-main check\n' >&2
  fi

  printf 'deploy guard: main at %s, working tree clean\n' "$head" >&2
}

#!/bin/bash

set -e

git_status=$(git status --porcelain=v1 2>/dev/null | wc -l)
if [ $git_status -ne 0 ]; then
    git status
    echo ""
    echo "Are you sure? You have uncommitted changes, consider using scripts/update-branch.sh."
    exit 1
fi

branch=$(git branch --show-current)
git fetch --prune --all --force --tags
git update-ref refs/heads/main origin/main
git checkout main
git branch -D $branch

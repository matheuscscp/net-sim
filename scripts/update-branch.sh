#!/bin/bash

set -e

git add .
git stash
git fetch --prune --all --force --tags
git update-ref refs/heads/main origin/main
git rebase main
git stash pop

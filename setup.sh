#!/bin/bash
# Viam cloud build setup: install Node 24 via mise, then project deps.
set -e

command -v mise &> /dev/null || curl https://mise.run | sh
export PATH="$HOME/.local/bin:$PATH"
eval "$(mise activate bash)"
mise use -g node@24

make setup

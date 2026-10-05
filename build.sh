#!/bin/bash

set -e

# The Viam cloud build runs this in a fresh shell where mise (installed by
# setup.sh) is not yet activated. Put mise on PATH and activate it so its
# managed Node/npm are available.
export PATH="$HOME/.local/bin:$PATH"

echo "🔧 Setting up mise shell integration..."
eval "$(mise activate bash)"

make module.tar.gz

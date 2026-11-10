#!/bin/bash
# Copyright 2026 Christopher O'Connell
# SPDX-License-Identifier: Apache-2.0

# Connectivity harness: one tiny request (and one JSON-mode request) to each
# inference provider whose key is present. Spend is a few hundred tokens.
#
# Usage:
#   SPEC_ENV_FILE=path/to/.env ./scripts/integration.sh [go test flags]
#   SPEC_ENV_FILE=path/to/.env ./scripts/integration.sh doctor [flags]
#
# The second form runs `spec doctor` (its live check) with the same keys,
# against the user config in XDG_CONFIG_HOME.
#
# SPEC_ENV_FILE names a file of VAR=value lines (the provider keys, never
# committed); it is sourced into this process only. Without it, the keys
# must already be in the environment. Keys are never printed.

set -euo pipefail

cd "$(dirname "$0")/.."

if [ -n "${SPEC_ENV_FILE:-}" ]; then
    if [ ! -r "$SPEC_ENV_FILE" ]; then
        echo "integration.sh: cannot read SPEC_ENV_FILE=$SPEC_ENV_FILE" >&2
        exit 1
    fi
    set -a
    # shellcheck disable=SC1090
    . "$SPEC_ENV_FILE"
    set +a
fi

if [ "${1:-}" = "doctor" ]; then
    exec go run ./cmd/spec "$@"
fi

export SPEC_INTEGRATION=1
exec go test -count=1 -v -run '^TestIntegration' "$@" ./internal/infer/

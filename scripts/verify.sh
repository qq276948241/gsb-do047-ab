#!/usr/bin/env bash
#
# One-command repository acceptance for mdschema.
#
# Runs, in order, stopping at the first failure:
#   1. language/dependency lock check (go.mod declarations vs go.sum)
#   2. dependency download at the exact locked versions
#   3. build
#   4. tests
#   5. mdschema check on every bundled example
#   6. mdschema generate for every example, diffed against committed golden text
#
# On a clean machine this is the only command needed:
#   ./scripts/verify.sh
#
set -euo pipefail

cd "$(dirname "$0")/.."

exec go run ./scripts/verify

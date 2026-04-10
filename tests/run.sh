#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for test_file in "$ROOT_DIR"/tests/*.sh; do
    case "$(basename "$test_file")" in
        run.sh|testlib.sh)
            continue
            ;;
    esac

    printf '==> %s\n' "$(basename "$test_file")"
    bash "$test_file"
done

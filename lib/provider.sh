#!/usr/bin/env bash
#
# provider.sh — coda auth and provider management
#
# Providers live in directories:
#   $_CODA_DIR/providers/<mode>/     (builtin)
#   $CODA_PROVIDERS_DIR/<mode>/      (user, ~/.config/coda/providers/)
#
# Each provider directory contains:
#   auth.sh    — defines _provider_auth()
#   status.sh  — defines _provider_status()

CODA_PROVIDERS_DIR="${CODA_PROVIDERS_DIR:-$HOME/.config/coda/providers}"

_coda_auth() {
    local mode
    mode=$(_coda_provider_mode) || return 1

    _coda_load_provider "$mode" auth || return 1

    if ! declare -f _provider_auth &>/dev/null; then
        echo "Provider '$mode' does not define _provider_auth()."
        return 1
    fi

    _provider_auth
}

_coda_provider() {
    local subcmd="${1:-help}"

    case "$subcmd" in
        status) _coda_provider_status ;;
        ls)     _coda_provider_ls ;;
        ""|help)
            echo "Usage: coda provider <status|ls>"
            ;;
        *)
            echo "Unknown provider subcommand: $subcmd"
            echo "Usage: coda provider <status|ls>"
            return 1
            ;;
    esac
}

_coda_provider_status() {
    local mode
    mode=$(_coda_provider_mode) || return 1

    _coda_load_provider "$mode" status || return 1

    if ! declare -f _provider_status &>/dev/null; then
        echo "Provider '$mode' does not define _provider_status()."
        return 1
    fi

    _provider_status
}

_coda_provider_ls() {
    echo "Available providers:"
    local seen="" name source
    for d in "$CODA_PROVIDERS_DIR"/*/  "$_CODA_DIR/providers"/*/; do
        [ -d "$d" ] || continue
        name=$(basename "$d")
        case "$seen" in *"|$name|"*) continue ;; esac
        seen="$seen|$name|"
        if [ -d "$CODA_PROVIDERS_DIR/$name" ]; then
            source="user"
        else
            source="builtin"
        fi
        local active=""
        if [ "$name" = "${CODA_PROVIDER_MODE:-claude-auth}" ]; then
            active=" (active)"
        fi
        echo "  $name  ($source)$active"
    done
    for name in "${!_CODA_PLUGIN_PROVIDERS[@]}"; do
        case "$seen" in *"|$name|"*) continue ;; esac
        seen="$seen|$name|"
        local active=""
        if [ "$name" = "${CODA_PROVIDER_MODE:-claude-auth}" ]; then
            active=" (active)"
        fi
        echo "  $name  (plugin)$active"
    done
}

_coda_provider_mode() {
    local mode="${CODA_PROVIDER_MODE:-claude-auth}"

    if _coda_find_provider_dir "$mode" >/dev/null; then
        echo "$mode"
        return 0
    fi

    echo "Unknown provider: $mode" >&2
    echo "Available: $(_coda_list_providers | tr '\n' ' ')" >&2
    echo "Providers live in: $CODA_PROVIDERS_DIR/" >&2
    return 1
}

_coda_find_provider_dir() {
    local mode="$1"
    if [ -d "$CODA_PROVIDERS_DIR/$mode" ]; then
        echo "$CODA_PROVIDERS_DIR/$mode"
    elif [ -d "$_CODA_DIR/providers/$mode" ]; then
        echo "$_CODA_DIR/providers/$mode"
    elif [[ -n "${_CODA_PLUGIN_PROVIDERS[$mode]+x}" ]]; then
        echo "${_CODA_PLUGIN_PROVIDERS[$mode]}"
    else
        return 1
    fi
}

_coda_load_provider() {
    local mode="$1" component="$2"

    local provider_dir
    provider_dir=$(_coda_find_provider_dir "$mode") || {
        echo "Provider '$mode' not found."
        echo "Available: $(_coda_list_providers | tr '\n' ' ')"
        echo "Providers live in: $CODA_PROVIDERS_DIR/"
        return 1
    }

    local component_file="$provider_dir/${component}.sh"
    if [ ! -f "$component_file" ]; then
        echo "Provider '$mode' is missing ${component}.sh"
        echo "Expected: $component_file"
        return 1
    fi

    unset -f _provider_auth _provider_status 2>/dev/null

    # shellcheck source=/dev/null
    source "$component_file"
}

_coda_list_providers() {
    local seen="" name
    for d in "$CODA_PROVIDERS_DIR"/*/ "$_CODA_DIR/providers"/*/; do
        [ -d "$d" ] || continue
        name=$(basename "$d")
        case "$seen" in *"|$name|"*) continue ;; esac
        seen="$seen|$name|"
        echo "$name"
    done
    for name in "${!_CODA_PLUGIN_PROVIDERS[@]}"; do
        case "$seen" in *"|$name|"*) continue ;; esac
        seen="$seen|$name|"
        echo "$name"
    done
}

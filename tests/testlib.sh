#!/usr/bin/env bash

set -euo pipefail

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

assert_eq() {
    local expected="$1"
    local actual="$2"
    local message="${3:-expected values to match}"

    if [ "$expected" != "$actual" ]; then
        fail "$message (expected: '$expected', actual: '$actual')"
    fi
}

assert_contains() {
    local haystack="$1"
    local needle="$2"
    local message="${3:-expected output to contain substring}"

    case "$haystack" in
        *"$needle"*) ;;
        *) fail "$message (missing: '$needle')" ;;
    esac
}

assert_not_contains() {
    local haystack="$1"
    local needle="$2"
    local message="${3:-expected output to omit substring}"

    case "$haystack" in
        *"$needle"*) fail "$message (unexpected: '$needle')" ;;
    esac
}

assert_file_exists() {
    local path="$1"
    local message="${2:-expected file to exist}"

    [ -e "$path" ] || fail "$message ($path)"
}

assert_not_exists() {
    local path="$1"
    local message="${2:-expected path to be absent}"

    [ ! -e "$path" ] || fail "$message ($path)"
}

wait_until_gone() {
    local path="$1"
    local attempts="${2:-40}"
    local delay="${3:-0.1}"

    while [ "$attempts" -gt 0 ]; do
        [ ! -e "$path" ] && return 0
        attempts=$((attempts - 1))
        sleep "$delay"
    done

    fail "timed out waiting for path to disappear: $path"
}

wait_until_command() {
    local attempts="$1"
    local delay="$2"
    shift 2

    while [ "$attempts" -gt 0 ]; do
        if "$@"; then
            return 0
        fi
        attempts=$((attempts - 1))
        sleep "$delay"
    done

    fail "timed out waiting for command: $*"
}

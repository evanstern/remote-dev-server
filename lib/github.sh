#!/usr/bin/env bash
#
# github.sh — GitHub App identity for bot comments
#

CODA_GITHUB_CLIENT_ID="${CODA_GITHUB_CLIENT_ID:-}"
CODA_GITHUB_INSTALLATION_ID="${CODA_GITHUB_INSTALLATION_ID:-}"
CODA_GITHUB_PRIVATE_KEY_PATH="${CODA_GITHUB_PRIVATE_KEY_PATH:-$HOME/.config/coda/github-app-private-key.pem}"

_coda_github() {
    local subcmd="${1:-}"
    shift 2>/dev/null || true

    case "$subcmd" in
        token)    _coda_github_token "$@" ;;
        comment)  _coda_github_comment "$@" ;;
        status)   _coda_github_status ;;
        *)        _coda_github_help; return 1 ;;
    esac
}

_coda_github_check_config() {
    if [ -z "$CODA_GITHUB_CLIENT_ID" ]; then
        echo "CODA_GITHUB_CLIENT_ID is not set. Add it to your .env file."
        return 1
    fi
    if [ -z "$CODA_GITHUB_INSTALLATION_ID" ]; then
        echo "CODA_GITHUB_INSTALLATION_ID is not set. Add it to your .env file."
        return 1
    fi
    if [ ! -f "$CODA_GITHUB_PRIVATE_KEY_PATH" ]; then
        echo "Private key not found: $CODA_GITHUB_PRIVATE_KEY_PATH"
        echo "Download it from GitHub App settings and place it there."
        return 1
    fi
    if ! command -v coda-core &>/dev/null; then
        echo "coda-core not found. Run install.sh to build it."
        return 1
    fi
}

_coda_github_token() {
    _coda_github_check_config || return 1

    coda-core github token \
        --client-id "$CODA_GITHUB_CLIENT_ID" \
        --installation-id "$CODA_GITHUB_INSTALLATION_ID" \
        --key "$CODA_GITHUB_PRIVATE_KEY_PATH"
}

_coda_github_comment() {
    _coda_github_check_config || return 1

    local repo="" issue="" body=""

    while [ $# -gt 0 ]; do
        case "$1" in
            --repo|-r)   repo="$2"; shift 2 ;;
            --issue|-i)  issue="$2"; shift 2 ;;
            --body|-b)   body="$2"; shift 2 ;;
            *)           shift ;;
        esac
    done

    if [ -z "$repo" ]; then
        repo=$(_coda_github_detect_repo)
        if [ -z "$repo" ]; then
            echo "Could not detect repository. Use --repo owner/repo."
            return 1
        fi
    fi

    if [ -z "$issue" ]; then
        echo "--issue is required."
        return 1
    fi

    local comment_args=(
        --client-id "$CODA_GITHUB_CLIENT_ID"
        --installation-id "$CODA_GITHUB_INSTALLATION_ID"
        --key "$CODA_GITHUB_PRIVATE_KEY_PATH"
        --repo "$repo"
        --issue "$issue"
    )

    if [ -n "$body" ]; then
        comment_args+=(--body "$body")
        coda-core github comment "${comment_args[@]}"
    else
        coda-core github comment "${comment_args[@]}"
    fi
}

_coda_github_detect_repo() {
    local remote_url
    remote_url=$(git remote get-url "${GIT_REMOTE:-origin}" 2>/dev/null) || return

    remote_url="${remote_url%.git}"
    if [[ "$remote_url" =~ github\.com[:/]([^/]+/[^/]+)$ ]]; then
        echo "${BASH_REMATCH[1]}"
    fi
}

_coda_github_status() {
    echo "GitHub App Configuration:"
    echo "  Client ID:       ${CODA_GITHUB_CLIENT_ID:-<not set>}"
    echo "  Installation ID: ${CODA_GITHUB_INSTALLATION_ID:-<not set>}"
    echo "  Private key:     $CODA_GITHUB_PRIVATE_KEY_PATH"

    if [ -f "$CODA_GITHUB_PRIVATE_KEY_PATH" ]; then
        echo "  Key file:        found"
    else
        echo "  Key file:        missing"
    fi

    if command -v coda-core &>/dev/null; then
        echo "  coda-core:       found"
    else
        echo "  coda-core:       missing"
    fi

    if [ -n "$CODA_GITHUB_CLIENT_ID" ] && [ -n "$CODA_GITHUB_INSTALLATION_ID" ] && \
       [ -f "$CODA_GITHUB_PRIVATE_KEY_PATH" ] && command -v coda-core &>/dev/null; then
        echo ""
        echo "Testing token generation..."
        local token
        if token=$(_coda_github_token 2>&1); then
            echo "  Token: OK (${#token} chars)"
        else
            echo "  Token: FAILED"
            echo "  $token"
        fi
    fi
}

_coda_github_help() {
    cat <<'EOF'
coda github — post comments as the Coda bot identity

USAGE
  coda github token                            Print an installation access token
  coda github comment --issue N [--repo R] [--body B]
                                                Post a comment (reads stdin if no --body)
  coda github status                           Check configuration and test token

OPTIONS
  --repo, -r   owner/repo  (auto-detected from git remote if omitted)
  --issue, -i  Issue or PR number
  --body, -b   Comment text (reads stdin if omitted)

CONFIGURATION (.env)
  CODA_GITHUB_CLIENT_ID            GitHub App Client ID (recommended over App ID)
  CODA_GITHUB_INSTALLATION_ID      Installation ID
  CODA_GITHUB_PRIVATE_KEY_PATH     Path to PEM file (default: ~/.config/coda/github-app-private-key.pem)
EOF
}

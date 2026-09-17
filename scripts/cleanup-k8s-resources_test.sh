#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
CLEANUP_SCRIPT="${SCRIPT_DIR}/cleanup-k8s-resources.sh"
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/hyperfleet-cleanup-test.XXXXXX")
trap 'rm -rf "${TEST_DIR}"' EXIT

assert_equal() {
    local expected="$1"
    local actual="$2"
    local message="$3"

    if [[ "$expected" != "$actual" ]]; then
        printf 'FAIL: %s\nexpected:\n%s\nactual:\n%s\n' "$message" "$expected" "$actual" >&2
        exit 1
    fi
}

assert_contains() {
    local haystack="$1"
    local needle="$2"
    local message="$3"

    if [[ "$haystack" != *"$needle"* ]]; then
        printf 'FAIL: %s\nmissing: %s\noutput:\n%s\n' "$message" "$needle" "$haystack" >&2
        exit 1
    fi
}

assert_not_contains() {
    local haystack="$1"
    local needle="$2"
    local message="$3"

    if [[ "$haystack" == *"$needle"* ]]; then
        printf 'FAIL: %s\nunexpected: %s\noutput:\n%s\n' "$message" "$needle" "$haystack" >&2
        exit 1
    fi
}

mkdir -p "${TEST_DIR}/bin"

cat >"${TEST_DIR}/bin/helm" <<'EOF'
#!/usr/bin/env bash

printf '%s\n' "$*" >> "${HELM_CALLS_FILE:?}"
if [[ "${HELM_FAIL_RELEASE:-}" == "${2:-}" ]]; then
    exit 1
fi
EOF

cat >"${TEST_DIR}/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

chmod +x "${TEST_DIR}/bin/helm" "${TEST_DIR}/bin/kubectl"

cat >"${TEST_DIR}/releases.json" <<'EOF'
[
  {
    "name": "hyperfleet-api",
    "namespace": "test",
    "installed": true,
    "labels": "chart:api, group:hyperfleet,component:api"
  },
  {
    "name": "hyperfleet-mock-oidc",
    "namespace": "test",
    "installed": false,
    "labels": "group:hyperfleet"
  },
  {
    "name": "legacy-release",
    "namespace": "test",
    "labels": "group:hyperfleet"
  },
  {
    "name": "hyperfleet-backup",
    "namespace": "test",
    "installed": true,
    "labels": "group:hyperfleet-backup"
  },
  {
    "name": "hyperfleet-owner-prefix",
    "namespace": "test",
    "installed": true,
    "labels": "owner:group:hyperfleet"
  },
  {
    "name": "unrelated-release",
    "namespace": "test",
    "installed": true,
    "labels": "group:other"
  }
]
EOF

export PATH="${TEST_DIR}/bin:${PATH}"
export HELM_CALLS_FILE="${TEST_DIR}/helm-calls"

output=$("${CLEANUP_SCRIPT}" -n test -f "${TEST_DIR}/releases.json" 2>&1)
expected_calls=$'uninstall hyperfleet-api -n test\nuninstall legacy-release -n test'
assert_equal "$expected_calls" "$(cat "${HELM_CALLS_FILE}")" \
    "only enabled HyperFleet releases are uninstalled"
assert_contains "$output" "All cleanup operations completed successfully" \
    "successful cleanup is reported"
assert_not_contains "$output" "hyperfleet-mock-oidc" \
    "disabled release is not mentioned"
assert_not_contains "$output" "hyperfleet-backup" \
    "near-match group label is not mentioned"
assert_not_contains "$output" "hyperfleet-owner-prefix" \
    "owner-prefixed label is not mentioned"
assert_not_contains "$output" "unrelated-release" \
    "non-HyperFleet release is not mentioned"

: >"${HELM_CALLS_FILE}"
if output=$(HELM_FAIL_RELEASE=hyperfleet-api "${CLEANUP_SCRIPT}" -n test -f "${TEST_DIR}/releases.json" 2>&1); then
    printf 'FAIL: cleanup should fail when helm uninstall fails\n' >&2
    exit 1
fi

assert_equal "$expected_calls" "$(cat "${HELM_CALLS_FILE}")" \
    "cleanup continues attempting selected releases after an uninstall failure"
assert_contains "$output" "Failed to uninstall hyperfleet-api" \
    "the helm uninstall failure is reported"
assert_contains "$output" "Cleanup completed with errors" \
    "cleanup returns an error summary"

printf 'PASS: cleanup release filtering and uninstall failure handling\n'

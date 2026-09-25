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

case "$1" in
    list)
        printf '%s\n' "$*" >> "${HELM_LIST_CALLS_FILE:?}"
        if [[ -n "${HELM_LIST_STDERR:-}" ]]; then
            printf '%s\n' "${HELM_LIST_STDERR}" >&2
        fi
        if [[ -n "${HELM_LIST_EXIT:-}" ]]; then
            printf '%s\n' "${HELM_LIST_OUTPUT:-}" >&2
            exit "${HELM_LIST_EXIT}"
        fi
        printf '%s\n' "${HELM_LIST_OUTPUT:-}"
        ;;
    uninstall)
        printf '%s\n' "$*" >> "${HELM_CALLS_FILE:?}"
        if [[ "${HELM_FAIL_RELEASE:-}" == "${2:-}" ]]; then
            exit 1
        fi
        ;;
esac
EOF

cat >"${TEST_DIR}/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${KUBECTL_CALLS_FILE:?}"
if [[ "$1" == "get" && -n "${KUBECTL_GET_EXIT:-}" ]]; then
    printf '%s\n' "${KUBECTL_GET_OUTPUT:-}"
    exit "${KUBECTL_GET_EXIT}"
fi
if [[ "$1" == "delete" && -n "${KUBECTL_DELETE_FAIL:-}" ]]; then
    exit 1
fi
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
export HELM_LIST_CALLS_FILE="${TEST_DIR}/helm-list-calls"
export KUBECTL_CALLS_FILE="${TEST_DIR}/kubectl-calls"

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
assert_contains "$output" "was deleted but some releases failed to uninstall" \
    "the partial cleanup outcome is reported"

printf 'PASS: cleanup release filtering and uninstall failure handling\n'

missing_json="${TEST_DIR}/does-not-exist.json"

: >"${HELM_CALLS_FILE}"
: >"${HELM_LIST_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(HELM_LIST_OUTPUT="orphan-release" "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1)
assert_contains "$output" "missing or empty" \
    "missing JSON file is reported"
assert_contains "$output" "falling back to best-effort cleanup" \
    "fallback path is logged as a warning"
assert_contains "$(cat "${HELM_LIST_CALLS_FILE}")" "--pending" \
    "discovery includes pending releases"
assert_contains "$(cat "${HELM_CALLS_FILE}")" "uninstall orphan-release -n test" \
    "best-effort cleanup uninstalls releases discovered via helm list"
assert_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace test --wait --timeout=5m" \
    "the namespace is still deleted"
assert_contains "$output" "All cleanup operations completed successfully" \
    "fallback cleanup reports success"

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(HELM_LIST_OUTPUT="orphan-release" HELM_FAIL_RELEASE="orphan-release" \
    "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1)
assert_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace test --wait --timeout=5m" \
    "the namespace is still deleted"
assert_contains "$output" "was deleted, but some releases failed to uninstall during best-effort cleanup" \
    "the incomplete cleanup is reported"
assert_not_contains "$output" "All cleanup operations completed successfully" \
    "full success is not reported"

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

if output=$(HELM_LIST_OUTPUT="orphan-release" HELM_FAIL_RELEASE="orphan-release" \
    KUBECTL_DELETE_FAIL=1 "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1); then
    printf 'FAIL: cleanup should fail when the namespace delete fails\n' >&2
    exit 1
fi

assert_contains "$output" "Cleanup completed with errors" \
    "the namespace delete failure is reported"

empty_json="${TEST_DIR}/empty.json"
: > "${empty_json}"

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(HELM_LIST_OUTPUT="orphan-release" "${CLEANUP_SCRIPT}" -n test -f "${empty_json}" 2>&1)
assert_contains "$output" "missing or empty" \
    "empty JSON file is reported"
assert_contains "$(cat "${HELM_CALLS_FILE}")" "uninstall orphan-release -n test" \
    "best-effort cleanup uninstalls releases discovered via helm list"

printf 'PASS: cleanup falls back to best-effort cleanup when the JSON file is missing or empty\n'

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

if output=$(HELM_LIST_OUTPUT="orphan-release" HELM_FAIL_RELEASE="orphan-release" \
    "${CLEANUP_SCRIPT}" -n test 2>&1); then
    printf 'FAIL: cleanup should fail when a deliberate best-effort run has a failed uninstall\n' >&2
    exit 1
fi

assert_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace test --wait --timeout=5m" \
    "the namespace is still deleted"
assert_contains "$output" "Cleanup completed with errors" \
    "the cleanup failure is reported"

printf 'PASS: a deliberate best-effort run (no -f) still fails on an uninstall failure\n'

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(KUBECTL_GET_EXIT=1 KUBECTL_GET_OUTPUT='Error from server (NotFound): namespaces "test" not found' \
    "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1)
assert_contains "$output" "Namespace 'test' does not exist" \
    "the missing namespace is reported"
assert_not_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace" \
    "delete is not attempted"
assert_contains "$output" "All cleanup operations completed successfully" \
    "overall success is still reported"

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

if output=$(KUBECTL_GET_EXIT=1 KUBECTL_GET_OUTPUT='Error from server (Forbidden): namespaces "test" is forbidden' \
    "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1); then
    printf 'FAIL: cleanup should fail when the namespace existence check errors for a non-NotFound reason\n' >&2
    exit 1
fi

assert_not_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace" \
    "delete is not attempted"
assert_contains "$output" "Failed to check namespace 'test'" \
    "the existence-check failure is reported"
assert_contains "$output" "Cleanup completed with errors" \
    "the cleanup failure is reported"

printf 'PASS: cleanup distinguishes NotFound from other namespace existence-check errors\n'

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(HELM_LIST_EXIT=1 HELM_LIST_OUTPUT='Error: Kubernetes cluster unreachable' \
    "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1)
assert_contains "$output" "Failed to list Helm releases in namespace test: Error: Kubernetes cluster unreachable" \
    "the helm list failure is reported with its error text"
assert_not_contains "$output" "No Helm releases found in namespace test" \
    "an empty namespace is not reported"
assert_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace test --wait --timeout=5m" \
    "the namespace is still deleted"
assert_not_contains "$output" "All cleanup operations completed successfully" \
    "full success is not reported"

printf 'PASS: cleanup reports a failed helm list as incomplete cleanup\n'

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

corrupt_json="${TEST_DIR}/corrupt-releases.json"
printf '{not valid json' > "${corrupt_json}"

if output=$("${CLEANUP_SCRIPT}" -n test -f "${corrupt_json}" 2>&1); then
    printf 'FAIL: cleanup should fail when the release JSON cannot be parsed\n' >&2
    exit 1
fi

assert_contains "$output" "Failed to parse Helm releases from ${corrupt_json}" \
    "the parse failure is reported"
assert_contains "$(cat "${KUBECTL_CALLS_FILE}")" "delete namespace test --wait --timeout=5m" \
    "the namespace is still deleted"
assert_contains "$output" "Cleanup completed with errors" \
    "the cleanup failure is reported"

printf 'PASS: cleanup handles a malformed helm-release JSON without leaking the namespace\n'

: >"${HELM_CALLS_FILE}"
: >"${KUBECTL_CALLS_FILE}"

output=$(HELM_LIST_OUTPUT="orphan-release" HELM_LIST_STDERR="WARNING: Kubernetes configuration file is group-readable" \
    "${CLEANUP_SCRIPT}" -n test -f "${missing_json}" 2>&1)
assert_equal "uninstall orphan-release -n test" "$(cat "${HELM_CALLS_FILE}")" \
    "only the real release is uninstalled"
assert_contains "$output" "All cleanup operations completed successfully" \
    "a stderr warning does not cause a spurious failure"

printf 'PASS: cleanup ignores stderr warnings from a successful helm list\n'

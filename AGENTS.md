# HyperFleet E2E — Agent Instructions

<!--
Maintainers: this file is loaded into every agent session (CLAUDE.md imports it), so every line costs context.
- Keep it under 200 lines. For each line ask: "would removing this cause the agent to make a mistake?" If not, cut it.
- Keep only what an agent cannot learn by reading the code: commands, non-obvious conventions, gotchas.
  Do not list functions, files, or enum values here; they go stale. Point to the file instead.
- Link to docs/ rather than copying from it.
- Use IMPORTANT on one rule at most; when many lines are emphasized, none stands out.
-->

Black-box E2E tests for HyperFleet. Specs call the HyperFleet API, create ephemeral resources, check adapter execution and the K8s resources that result, then clean up. Go 1.26, Ginkgo v2, Gomega, and the generic HTTP client in `pkg/client`.

Spec suites live in `e2e/<suite>/`, one directory per suite. Each suite is registered by a blank import in `e2e/e2e.go`; add one there when you create a suite.

## Verification

Run `make check` before you call work done, then `make build`.

- `make check`: `generate`, `fmt-check`, `vet`, `lint`, unit tests, `verify-tools` (fails if `tools/go.mod` drifted)
- `make fmt`: `gofmt -s -w .`
- `make lint`: golangci-lint pinned in `tools/go.mod`, configured in `.golangci.yml`
- `make test`: unit tests for `./pkg/...` only. E2E specs need a live environment; see `docs/setup.md`
- `make list-tests`: dry run that lists specs by tier. Use it to check labels and names without a cluster

## Where to look

- Writing specs, full conventions: `docs/development.md`
- Architecture and package layout: `docs/architecture.md`
- Environment setup (kind, GCP, desire stack without Maestro): `docs/setup.md`
- Running tests, Prow jobs, label-to-CI mapping: `docs/runbook.md`
- CI failures and log locations: `docs/debugging.md`
- Test case documents and templates: `test-design/` (start at `test-design/README.md`)
- Which layer a test belongs in (unit / integration / E2E): [test placement strategy](https://github.com/openshift-hyperfleet/architecture/blob/main/hyperfleet/docs/e2e-testing/test-placement-strategy.md)
- Config defaults, struct, file: `pkg/config/defaults.go`, `pkg/config/config.go`, `configs/config.yaml`
- Pollers and matchers: `pkg/helper/pollers.go`, `pkg/helper/matchers.go`. Reuse these before writing new ones

## Writing E2E specs

### Files and names

- IMPORTANT: spec files end in `.go`, never `_test.go`. Specs are compiled into the `hyperfleet-e2e` binary, not run by `go test`. The only `_test.go` file in `e2e/` is `e2e/e2e_suite_test.go`, the ginkgo CLI entry point for `make e2e-ci`. Put no spec code there.
- Path: `e2e/<suite>/descriptive-name.go`. The package name matches the directory.
- Describe name: `[Suite: <suite>][<category>] Description`, for example `[Suite: cluster][baseline] Cluster Resource Type Lifecycle`. Match the categories already used in that suite.

### Labels (from `pkg/labels`, never string literals)

- Every spec carries exactly one severity label: `labels.Tier0` (blocks release), `labels.Tier1` (important), `labels.Tier2` (edge case, can defer).
- `make test` enforces this: `pkg/labels/validate_test.go` parses every spec in `e2e/`.
- Optional labels are in `pkg/labels/labels.go`.

### Waiting on async state

Use `Eventually` with a poller and a matcher in the spec itself. Do not write `WaitFor*` helpers that hide `Eventually`.

```go
Eventually(h.PollCluster(ctx, clusterID), h.Cfg.Timeouts.Cluster.Reconciled, h.Cfg.Polling.Interval).
    Should(helper.HaveResourceCondition(client.ConditionTypeReconciled, client.ResourceConditionStatusTrue))
```

- For a one-off compound check, use `Eventually(func(g Gomega) { g.Expect(...) }).Should(Succeed())`. Inside the closure, call `g.Expect`, not bare `Expect`.
- Take timeouts and intervals from `h.Cfg.Timeouts.*` and `h.Cfg.Polling.Interval`. Never hardcode durations.
- Mark major steps with `ginkgo.By()`, but never inside an `Eventually` closure.

### Cleanup

- Register `ginkgo.DeferCleanup` right after you create each resource. Helpers such as `h.DeferClusterCleanup` exist in `pkg/helper/helper.go`.
- Exception: in an `Ordered` container where a later spec uses a resource an earlier spec created, register the cleanup in `BeforeAll`. A `DeferCleanup` made inside an `It` runs when that `It` ends, before the later specs run. Say so in a comment, as `e2e/adapter/adapter_with_desire.go` does.

### Payloads

- Resolve payload paths with `h.TestDataPath("payloads/...")`. Never hardcode a `testdata/` prefix, because CI overrides `TESTDATA_DIR`.
- Payloads in `testdata/payloads/` are Go templates. The available variables are the `templateVars` struct in `pkg/client/payload.go`.

### Audit identity (JWT)

When a spec checks `created_by` / `deleted_by`, guard the check. Audit checks are skipped when no identity is configured:

```go
if expected := h.ExpectedIdentity(); expected != "" {
    Expect(cluster).To(helper.HaveAuditIdentity(expected))
}
```

## Gotchas

- Adapter names come from config at runtime: `h.Cfg.Adapters.Cluster` and `h.Cfg.Adapters.NodePool`. Never hardcode them. `configs/config.yaml` (for example `cl-namespace`) overrides the compiled defaults in `pkg/config/defaults.go` (for example `clusters-namespace`).
- Config priority: CLI flags > `HYPERFLEET_*` env vars > `configs/config.yaml` > `pkg/config/defaults.go`. Config file path: `--config` > `HYPERFLEET_CONFIG` > `./configs/config.yaml`.
- `helper.New()` calls `log.Fatalf` when the suite config is nil; call `helper.SetSuiteConfig` first. `Config.Validate()` returns an error rather than panicking. Outside `--dry-run` it requires `API.URL` and a `RunID` that is a valid K8s label value (`RUN_ID`), and it always checks `brokerType`.
- The API runs with JWT enabled by default. Set `identity.tokenRequest.serviceAccountName` and `.namespace`. The framework then gets a token from the K8s TokenRequest API at startup and sends it as `Authorization: Bearer` on every request.
- Logging uses `log/slog` directly. There is no custom logger wrapper.

## Boundaries

- Never import `e2e/*` packages from `pkg/`.
- Keep E2E specs to user journeys and critical operations. Field validation and single-component behavior belong in unit or integration tests (see the test placement strategy above).

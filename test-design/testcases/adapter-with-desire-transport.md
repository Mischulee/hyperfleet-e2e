# Feature: Adapter Framework - Desire Transport without Maestro

> **Status:** temporary. These tests prove that HyperFleet delivers resources through hyperfleet-applier with no Maestro. They run by hand on a local kind stack; no CI job runs them. Once HYPERFLEET-1503 switches the deployed stack from Maestro to the applier, HYPERFLEET-1505 removes this suite, and a green tier0 run becomes the proof that delivery works.

## Table of Contents

1. [Desire delivery stack runs with no Maestro component in the delivery path](#test-title-desire-delivery-stack-runs-with-no-maestro-component-in-the-delivery-path)
2. [Adapter creates a cluster's resources through the applier and reports their status](#test-title-adapter-creates-a-clusters-resources-through-the-applier-and-reports-their-status)
3. [Adapter deletes a cluster's resources through the applier before the cluster is finalized](#test-title-adapter-deletes-a-clusters-resources-through-the-applier-before-the-cluster-is-finalized)

---

## Overview

The delivery chain under test: HyperFleet API → Sentinel → adapter on the `remote` transport → desire store (Redis) → hyperfleet-applier → Kubernetes. The adapter writes an ApplyDesire and a ReadDesire per resource. The applier applies the resource and copies the live object into the ReadDesire's status, and the adapter builds its conditions from that copy.

The adapter is `cl-desire`, which runs the adapter repo's example [`charts/examples/remote-two-resources`](https://github.com/openshift-hyperfleet/hyperfleet-adapter/tree/main/charts/examples/remote-two-resources) unchanged. Its desires go to the store partition named after the run namespace. For a cluster `<id>` it manages:

| Object | Desire identity (group / resource / namespace / name) |
|--------|-------------------------------------------------------|
| Namespace `<id>-remote` | `""` / `namespaces` / `""` / `<id>-remote` |
| ConfigMap `cluster-config` | `""` / `configmaps` / `<id>-remote` / `cluster-config` |

The suite checks only what the API and the cluster show; it does not read the desire store. Leftover desire records are not visible to users, and the adapter's unit tests cover their cleanup (`internal/desireclient/cleanup_test.go` in hyperfleet-adapter). To inspect the store while debugging, see [Debugging](#debugging).

The suite has only the `desire-transport` label, no tier label, because the tier jobs deploy Maestro and no applier.

---

## Environment Setup

Bring up the stack and run the suite with [Setup Guide - Option 3](../../docs/setup.md#option-3-kind-with-desire-delivery-no-maestro). For the manual steps below, also set:

```bash
export API_URL=http://localhost:8000
export ADAPTER_NAME=cl-desire
```

---

## Test Title: Desire delivery stack runs with no Maestro component in the delivery path

### Description

This test validates that the run has the desire delivery components and nothing from Maestro. It fails when the stack was brought up without desire delivery, or when Maestro leaks into the run.

---

| **Field** | **Value** |
|-----------|-----------|
| **Pos/Neg** | Positive |
| **Priority** | No tier (label `desire-transport`) |
| **Status** | Draft |
| **Automation** | Automated |
| **Version** | MVP |
| **Created** | 2026-10-02 |
| **Updated** | 2026-10-05 |

---

### Preconditions

1. The stack was brought up with `DESIRE_DELIVERY_ENABLED=true`

---

### Test Steps

#### Step 1: Verify the desire stack's Deployments are Ready
**Action:**
```bash
for release in hyperfleet-api hyperfleet-gateway clusters nodepools cl-desire redis hyperfleet-applier; do
  kubectl get deploy -n ${NAMESPACE} -l app.kubernetes.io/instance=${release}
done
```

**Expected Result:**
- Every release has at least one Deployment, and each Deployment has all desired replicas, and at least one, updated and ready, with `Available=True`. A Deployment scaled to 0 fails this step.

#### Step 2: Verify nothing from Maestro is in the run
**Action:**
```bash
# Maestro, MQTT broker or OCM agent pods (the namespace is stripped first: the applier's pod name contains it)
kubectl get pods -n ${NAMESPACE} --no-headers -o custom-columns=NAME:.metadata.name \
  | sed "s/${NAMESPACE}//g" | grep -E 'maestro|mqtt|mosquitto|klusterlet|work-agent'

# The adapter's effective config
kubectl get configmap -n ${NAMESPACE} \
  -l app.kubernetes.io/instance=${ADAPTER_NAME},app.kubernetes.io/component=adapter-config \
  -o jsonpath='{.items[0].data.adapter-config\.yaml}' \
  | yq '{"maestro_client": .clients.maestro, "transport_types": [.transports[].type]}'
```

**Expected Result:**
- No Maestro, MQTT or OCM agent pod in the run namespace
- Exactly one adapter-config ConfigMap for `cl-desire`; `maestro_client` is `null`, and every transport is `remote`

---

## Test Title: Adapter creates a cluster's resources through the applier and reports their status

### Description

This test validates the create path end to end. Creating a cluster makes `cl-desire` hand its two objects to the applier through the store. The applier creates them and reports the live objects back, and the adapter builds its conditions from them, which lets the cluster reach `Reconciled=True`. `cl-desire` has no Kubernetes access of its own, so the objects on the cluster prove the applier created them. It fails when delivery through the applier, or the status it reports back, breaks.

---

| **Field** | **Value** |
|-----------|-----------|
| **Pos/Neg** | Positive |
| **Priority** | No tier (label `desire-transport`) |
| **Status** | Draft |
| **Automation** | Automated |
| **Version** | MVP |
| **Created** | 2026-10-02 |
| **Updated** | 2026-10-05 |

---

### Preconditions

1. The component set test passed
2. The API requires only `cl-desire` for clusters

---

### Test Steps

#### Step 1: Create a cluster
**Action:**
```bash
CLUSTER_ID=$(curl -s -X POST ${API_URL}/api/hyperfleet/v1/clusters \
  -H "Content-Type: application/json" \
  -d @testdata/payloads/clusters/cluster-request.json | jq -r '.id')
echo "CLUSTER_ID=${CLUSTER_ID}"
```

**Expected Result:**
- The API returns a cluster ID and `generation: 1`

#### Step 2: Wait for the cluster to become Reconciled
**Action:**
```bash
curl -s ${API_URL}/api/hyperfleet/v1/clusters/${CLUSTER_ID} \
  | jq '.status.conditions[] | select(.type == "Reconciled")'
```

**Expected Result:**
- `Reconciled` is `True` within `timeouts.cluster.reconciled`

#### Step 3: Verify the adapter status
**Action:**
```bash
curl -s ${API_URL}/api/hyperfleet/v1/clusters/${CLUSTER_ID}/statuses \
  | jq --arg a "${ADAPTER_NAME}" '.items[] | select(.adapter == $a)
      | {observed_generation, conditions: [.conditions[] | {type, status, reason}]}'
```

**Expected Result:**
- `observed_generation` equals the cluster generation (`1`)
- `Applied`, `Available` and `Health` are `True` (reasons `ResourcesObserved`, `LiveResourcesReady`, `Healthy` in the shipped example)

#### Step 4: Verify both objects exist on the cluster
**Action:**
```bash
kubectl get namespace ${CLUSTER_ID}-remote -o jsonpath='{.metadata.annotations}'
kubectl get configmap cluster-config -n ${CLUSTER_ID}-remote -o jsonpath='{.metadata.annotations}{"\n"}{.data}'
```

**Expected Result:**
- Both objects exist with `hyperfleet.io/generation: "1"`
- The ConfigMap has `cluster_id: ${CLUSTER_ID}`

---

## Test Title: Adapter deletes a cluster's resources through the applier before the cluster is finalized

### Description

This test validates the delete path. The adapter must see the applier confirm that both objects are gone before it reports `Finalized=True`, and the API hard-deletes the cluster inside the status update that completes finalization. So once the cluster returns 404, the Namespace check runs once, with no retry. A Namespace still there is a product finding: the adapter finalized early. Do not turn this check into polling.

---

| **Field** | **Value** |
|-----------|-----------|
| **Pos/Neg** | Positive |
| **Priority** | No tier (label `desire-transport`) |
| **Status** | Draft |
| **Automation** | Automated |
| **Version** | MVP |
| **Created** | 2026-10-02 |
| **Updated** | 2026-10-05 |

---

### Preconditions

1. The create test passed for `${CLUSTER_ID}`

---

### Test Steps

#### Step 1: Delete the cluster
**Action:**
```bash
curl -s -X DELETE ${API_URL}/api/hyperfleet/v1/clusters/${CLUSTER_ID} | jq '{deleted_time, generation}'
```

**Expected Result:**
- The API accepts the request and sets `deleted_time`

#### Step 2: Wait for the hard-delete
**Action:**
```bash
curl -s -o /dev/null -w '%{http_code}\n' ${API_URL}/api/hyperfleet/v1/clusters/${CLUSTER_ID}
```

**Expected Result:**
- The cluster returns `404` within `timeouts.cluster.deleted`

#### Step 3: Check once that the remote Namespace is gone
**Action:**
```bash
kubectl get namespace ${CLUSTER_ID}-remote
```

**Expected Result:**
- NotFound. A Namespace still in `Terminating` fails the test.

---

## Falsifiability

Each change below must turn the suite red. Never change an expected value to make a red run green.

| Change | Fails in |
|--------|----------|
| Scale the applier Deployment to 0 replicas | Create test: `Reconciled` times out |
| Bring the stack up without `DESIRE_DELIVERY_ENABLED=true` | Component set test |
| Make the example's `Finalized` status expression `is_deleting && adapter.?executionStatus.orValue("") == "success"` in the `cl-desire` task-config ConfigMap and restart the adapter | Delete test: the Namespace is not NotFound |
| Point the `remote-primary` transport's `target_cluster` at another literal | Create test: the applier never sees the desires, so `Reconciled` times out |

---

## Debugging

When a test fails, the desire store shows where the chain stopped. Each record is an `apply`, `read` or `delete` desire with the applier's `Successful` condition:

```bash
REDIS_POD=$(kubectl get pod -n ${NAMESPACE} -l app.kubernetes.io/instance=redis -o jsonpath='{.items[0].metadata.name}')

for key in $(kubectl exec -n ${NAMESPACE} ${REDIS_POD} -- redis-cli --scan --pattern "desire/{${NAMESPACE}/*${CLUSTER_ID}*"); do
  echo "${key}"
  kubectl exec -n ${NAMESPACE} ${REDIS_POD} -- redis-cli GET "${key}" | jq '{status: .status.conditions, read: .readStatus.conditions}'
done
```

The key layout is internal to the applier and may change; this is for people, not for assertions.

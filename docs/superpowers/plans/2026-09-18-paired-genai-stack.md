# Paired GenAI Stack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely deploy and restore the matching GenAI and Dashboard images for one Dashboard PR.

**Architecture:** Replace the one-component session with a stack transaction that atomically patches the two root RHOAI CSV related-image inputs. A command-scoped Kubernetes Lease and guarded JSON patches serialize mutations; a unique DSC annotation drives reconciliation.

**Tech Stack:** Go, Kubernetes/OpenShift CLI (`oc`), GitHub CLI (`gh`), standard-library JSON and filesystem APIs.

**Spec:** `docs/superpowers/specs/2026-09-18-paired-genai-stack-design.md`

## Global Constraints

- Support only the GenAI-plus-Dashboard profile and fail closed for unknown RHOAI layouts.
- Persist no credentials and modify no OGX, endpoint, Secret, RBAC, route, Pod, or PVC.
- Persist a mode-0600 session before any CSV mutation; failed rollouts remain recoverable.

---

### Task 1: Stack domain model and image resolution

**Files:**
- Modify: `internal/core/core.go`
- Modify: `internal/core/core_test.go`
- Modify: `internal/app/app.go`
- Test: `internal/app/app_test.go`

**Interfaces:**
- Produces `StackSession` with `Overrides []ImageOverride`, each holding the component, desired digest, original CSV value, and workload identity.
- Produces `ResolvePRStack(ctx, pr)` and `ResolveCustomStack(genAI, dashboard)`.

- [ ] Write failing tests for resolving one PR SHA into both CI image references, rejecting a lone custom image, and preserving both original values in a session.
- [ ] Run `go test ./internal/app ./internal/core` and confirm failures describe the missing stack behavior.
- [ ] Implement the minimal stack types, fixed GenAI/Dashboard profile, PR resolver, and digest pinning.
- [ ] Re-run `go test ./internal/app ./internal/core` and confirm success.
- [ ] Commit with `feat: model paired GenAI deployment stacks`.

### Task 2: Atomic CSV deployment transaction

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces `DiscoverStack(ctx, context)` and `PatchStackCSV(ctx, session, expected, desired)`.
- `Deploy` persists one session, performs one two-variable JSON patch, and waits for both workloads.

- [ ] Write failing fake-runner tests asserting discovery rejects ambiguous layouts and deployment emits one resource-version-guarded patch containing both image replacements.
- [ ] Run `go test ./internal/app` and confirm the atomic-patch assertions fail.
- [ ] Implement fail-closed Subscription/CSV/workload discovery and the single guarded CSV patch.
- [ ] Implement deploy/cleanup reconciliation nonces and paired workload readiness polling.
- [ ] Re-run `go test ./internal/app` and confirm success.
- [ ] Commit with `feat: deploy paired images atomically through RHOAI`.

### Task 3: Lease and complete guarded cleanup

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Produces acquire/release helpers for `coordination.k8s.io/v1 Lease` in the operator namespace.
- `Cleanup(ctx, sessionID)` restores both values or refuses without mutation when either value differs from session state.

- [ ] Write failing tests for Lease-scoped commands, already-restored idempotence, external-change refusal, reverse paired patching, and annotation deletion after readiness.
- [ ] Run `go test ./internal/app` and confirm each new cleanup behavior fails before implementation.
- [ ] Implement command-scoped Lease acquisition/release and the guarded paired cleanup state machine.
- [ ] Re-run `go test ./internal/app` and confirm success.
- [ ] Commit with `feat: guard paired cleanup with lease and image checks`.

### Task 4: Remove legacy mutation paths and document the contract

**Files:**
- Modify: `internal/app/app.go`
- Modify: `cmd/odh-pr-deploy/main.go`
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Test: `internal/app/app_test.go`

**Interfaces:**
- `deploy` accepts `--pr`, `--image`, and `--dashboard-image`; all other commands preserve their current session-based interface.

- [ ] Write a failing command test or source-level fake-runner assertion proving deploy never invokes PVC, Pod exec/cp, route, or direct Deployment patch commands.
- [ ] Run `go test ./...` and confirm the legacy-path assertion fails.
- [ ] Remove obsolete PVC/operator-pod/route helpers and unused flags/imports; update command help and docs with permissions, recovery, and non-goals.
- [ ] Run `gofmt -w` only on changed Go files, then `go test ./...` and `go vet ./...`.
- [ ] Commit with `refactor: remove legacy deployment mutation paths`.

### Task 5: Opt-in real-cluster transaction validation

**Files:**
- Modify: `README.md` only if command behavior differs from the documented contract.

- [ ] Build the CLI with `go build ./cmd/odh-pr-deploy`.
- [ ] On a contributor-controlled cluster, run `inspect`, deploy a published PR image pair, and confirm both target workload digests and Available conditions.
- [ ] Run `cleanup --session` and confirm both original release digests, readiness, removal of the unique annotation, and completed session state.
- [ ] If deployment or cleanup fails, retain state and diagnose from the persisted session; do not add a workaround that expands the ownership boundary.
- [ ] Commit only documentation corrections required by validated behavior.

# Paired GenAI Stack Deployment Design

## Goal

Deploy an ODH Dashboard GenAI PR as a compatible GenAI remote and Dashboard
host pair, then restore every resource change owned by the tool.

## Boundaries

The tool owns only the installed RHOAI operator CSV's two related-image values,
one unique `default-dsc` reconciliation annotation, and a short-lived
coordination Lease. It does not create or modify OGX, model endpoints, Secrets,
projects, application data, routes, cloned workloads, operator Pods, PVCs, or
cluster RBAC.

## Interface

`deploy --context CONTEXT --pr NUMBER` is the default GenAI-stack operation. It
resolves the PR head SHA once and derives the GenAI and Dashboard CI images from
that SHA. `deploy --context CONTEXT --image GENAI --dashboard-image DASHBOARD`
accepts an explicit compatible pair. A lone custom GenAI image is invalid.

`inspect`, `status --session`, `cleanup --session`, and `recover` remain. There
is no browser or API smoke-test command in this version.

## Transaction

The tool discovers exactly one RHOAI Subscription/CSV, both related-image
variables, the applications namespace, and the `gen-ai-ui` and
`rhods-dashboard` workloads. It verifies both images can be pinned to digests
before mutation. It writes one stack session to disk before acquiring or
changing cluster state.

While holding the transaction Lease, deploy applies a single JSON patch that
tests the CSV resource version and both original values before replacing both
values. It changes its unique DSC annotation to a deploy nonce and waits for
the root operator plus both target deployments to use the requested digests and
be Available. A failed wait leaves the persisted session and deployed state for
diagnosis.

Cleanup reacquires the Lease, checks both CSV values together, and either marks
an already-restored session complete, refuses a mixed/external change, or
applies one guarded patch restoring both original values. It changes the same
annotation to a cleanup nonce, waits for both original images and availability,
removes only that annotation, marks the session complete, and releases the
Lease.

## Compatibility and safety

Discovery is fail-closed: no guessed operator layouts, namespaces, variables,
or workloads. The Lease serializes concurrent command transactions; JSON-patch
tests remain the protection after a CLI process exits. Session files stay mode
0600 and never store credentials.

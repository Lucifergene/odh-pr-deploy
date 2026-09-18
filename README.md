# odh-pr-deploy

`odh-pr-deploy` is a contributor CLI for testing an ODH Dashboard GenAI pull
request on a contributor-controlled RHOAI cluster. It deploys the compatible
GenAI remote and Dashboard host together through the RHOAI operator, then can
restore precisely the inputs it changed.

It exists to make this workflow safe and repeatable:

```text
inspect → deploy paired PR images → test manually → cleanup → verify restoration
```

The existing Dashboard URL is unchanged; normal operator reconciliation serves
the selected PR images.

## Install

Build from a checkout with Go 1.27 or newer:

```bash
go build -o odh-pr-deploy ./cmd/odh-pr-deploy
./odh-pr-deploy inspect --context my-spare-cluster
```

`oc` must already be authenticated to the intended cluster. `gh` must be
authenticated to GitHub when using `--pr`.

## Safety boundary

The tool changes exactly two image values in the installed RHOAI operator CSV—GenAI and its matching Dashboard host—and adds one unique DataScienceCluster reconciliation annotation. It records both exact prior values before mutation. `cleanup` refuses concurrent changes, restores both values atomically, removes only its own annotation, and waits for both managed workloads to return to their original images.

It never manages routes, clones, OGX/Llama Stack, model endpoints, Secrets, projects, application data, or cluster RBAC. Missing application prerequisites are reported by the application, not repaired by this tool.

Use only on a contributor-controlled disposable cluster. Do not run it against a shared production cluster.

## Prerequisites

- Go 1.27+, `oc`, and `gh`, authenticated to the intended cluster and GitHub.
- Permission to get/patch the installed RHOAI operator CSV, create/delete a Lease in that namespace, annotate `default-dsc`, and get/watch the `gen-ai-ui` and `rhods-dashboard` Deployments.
- A successfully published Dashboard CI image for the requested PR.

The tool uses the installed `rhods-operator` Subscription and its CSV as the
source of truth. It stops before mutation if it cannot unambiguously find the
two required related-image inputs or the `gen-ai-ui` and `rhods-dashboard`
workloads.

## Usage

```bash
odh-pr-deploy inspect --context my-spare-cluster
odh-pr-deploy deploy --context my-spare-cluster --pr 9816
odh-pr-deploy deploy --context my-spare-cluster \
  --image quay.io/example/gen-ai:pr --dashboard-image quay.io/example/dashboard:pr
odh-pr-deploy status --session SESSION_ID
odh-pr-deploy cleanup --session SESSION_ID
odh-pr-deploy recover
```

`--pr` resolves both images from the same PR head SHA and pins their registry digests before cluster mutation. Custom GenAI images require an explicit matching Dashboard image. The applications namespace is discovered from the selected context unless `--namespace` is supplied. The tool fails closed when the expected RHOAI Subscription, CSV image inputs, or workloads are ambiguous or unavailable.

## Typical workflow

1. Inspect the cluster before changing it.

   ```bash
   odh-pr-deploy inspect --context my-spare-cluster
   ```

   Confirm the discovered namespace, CSV, original images, and workloads are
   the cluster you intend to use.

2. Deploy the PR and save the session ID printed in the JSON result.

   ```bash
   odh-pr-deploy deploy --context my-spare-cluster --pr 9816
   ```

3. Perform any manual application testing you need. Create and manage an OGX,
   model endpoint, credential, and test project independently; they remain
   outside this tool's lifecycle.

4. Restore the exact operator image inputs recorded for the session.

   ```bash
   odh-pr-deploy cleanup --session genai-stack-SESSION_ID
   ```

5. Use `status` to inspect the recorded session. If a terminal closed during a
   deployment, `recover` lists unfinished sessions that need cleanup.

## Cleanup contract

Cleanup is deliberately conservative. It reacquires the transaction Lease and
restores both image inputs only when both still contain this session's PR
digests. If another actor changed either image, cleanup stops and reports the
conflict instead of overwriting their work.

Cleanup is idempotent. If both inputs already contain their recorded original
values, it removes the tool's annotation and marks the session complete. It
does not roll back any resource it did not create or mutate.

## Verification and recovery

`deploy` verifies the operator rollout, both managed workload digests, and availability. It does not perform browser or application/API smoke tests.

If a terminal closes after deployment, run `recover` to identify unfinished
sessions, then `cleanup --session`. Session files are written with owner-only
permissions under `~/.local/state/odh-pr-deploy` by default; set
`ODH_PR_DEPLOY_STATE_DIR` to use a different local state directory.

## Troubleshooting

| Symptom | Meaning and action |
| --- | --- |
| Image is not found | Wait for the Dashboard PR CI image to publish, or use an explicit compatible image pair. |
| Lease acquisition fails | Another deployment or cleanup is running. Wait for it to finish; do not delete its Lease manually. |
| CSV/workload discovery fails | The cluster topology is unsupported or ambiguous. Do not override discovery manually; inspect the installation first. |
| Rollout times out | The session is retained for diagnosis. Inspect the operator/workloads, then run `cleanup --session` when it is safe. |
| Cleanup refuses a conflict | Another actor changed a tracked image. Compare the session record with the live CSV and resolve ownership before retrying. |

## Non-goals

This is not a general RHOAI installer or a Playground test framework. It does
not configure OGX, TrustyAI, model providers, external endpoints, RBAC, or
application fixtures. It also does not assert that a model returns a response;
that remains an explicit application-level test.

# odh-pr-deploy

Deploy a compatible GenAI Dashboard PR stack to a disposable OpenShift cluster through the RHOAI operator's `RELATED_IMAGE_*` inputs. The existing Dashboard URL is unchanged; normal operator reconciliation serves the selected PR images.

## Safety boundary

The tool changes exactly two image values in the installed RHOAI operator CSV—GenAI and its matching Dashboard host—and adds one unique DataScienceCluster reconciliation annotation. It records both exact prior values before mutation. `cleanup` refuses concurrent changes, restores both values atomically, removes only its own annotation, and waits for both managed workloads to return to their original images.

It never manages routes, clones, OGX/Llama Stack, model endpoints, Secrets, projects, application data, or cluster RBAC. Missing application prerequisites are reported by the application, not repaired by this tool.

Use only on a contributor-controlled disposable cluster. Do not run it against a shared production cluster.

## Prerequisites

- Go 1.27+, `oc`, and `gh`, authenticated to the intended cluster and GitHub.
- Permission to get/patch the installed RHOAI operator CSV, create/delete a Lease in that namespace, annotate `default-dsc`, and get/watch the `gen-ai-ui` and `rhods-dashboard` Deployments.
- A successfully published Dashboard CI image for the requested PR.

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

## Verification and recovery

`deploy` verifies the operator rollout, both managed workload digests, and availability. It does not perform browser or application/API smoke tests.

If a terminal closes after deployment, run `recover` to identify unfinished sessions, then `cleanup --session`. Cleanup refuses to overwrite an image override changed by another actor.

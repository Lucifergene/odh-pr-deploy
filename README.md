# odh-pr-deploy

Deploy a RHOAI Dashboard PR image to a disposable OpenShift cluster through the RHOAI operator's `RELATED_IMAGE_*` input. The existing Dashboard URL is unchanged; normal operator reconciliation serves the selected PR image.

## Safety boundary

The tool changes only one image value in the installed RHOAI operator CSV and adds a unique DataScienceCluster reconciliation annotation. It records the exact prior image before doing so. `cleanup` refuses concurrent changes, restores that value, removes only its own annotation, and waits for the managed workload to return to its original image. It never manages routes, clones, OGX/Llama Stack, model endpoints, Secrets, projects, or application data.

Use only on a contributor-controlled disposable cluster. Do not run it against a shared production cluster.

## Prerequisites

- Go 1.27+, `oc`, and `gh`, authenticated to the intended cluster and GitHub.
- Permission to get/patch the installed RHOAI operator CSV, annotate `default-dsc`, and get/watch the relevant managed Deployment.
- A successfully published Dashboard CI image for the requested PR.

## Usage

```bash
odh-pr-deploy deploy --context my-spare-cluster --component gen-ai --pr 9816
odh-pr-deploy deploy --context my-spare-cluster --component dashboard --image quay.io/example/image:tag
odh-pr-deploy status --session SESSION_ID
odh-pr-deploy cleanup --session SESSION_ID
odh-pr-deploy recover
```

Supported components are `gen-ai` and `dashboard` on RHOAI clusters. The applications namespace is discovered from the selected context unless `--namespace` is supplied.

## Verification and recovery

`deploy` verifies operator rollout, managed workload image digest, and availability. For a Playground PR, use a separately managed disposable test project and call the BFF Responses API with an OpenShift access token; browser testing is optional for UI-specific changes.

If a terminal closes after deployment, run `recover` to identify unfinished sessions, then `cleanup --session`. Cleanup refuses to overwrite an image override changed by another actor.

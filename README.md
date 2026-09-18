# odh-pr-deploy

`odh-pr-deploy` safely tests a Dashboard component PR image on an existing RHOAI cluster. It does not install or upgrade RHOAI, CatalogSources, Subscriptions, or CRDs.

## Commands

```bash
# Read only: confirm the selected component and cluster.
odh-pr-deploy inspect --context CONTEXT

# Default safe mode: create a selector-isolated, ownerless copy.
odh-pr-deploy deploy --context CONTEXT --component gen-ai --pr PR_NUMBER

# Managed mode is available only for workloads not owned by a controller.
odh-pr-deploy deploy --context CONTEXT --component gen-ai --pr PR_NUMBER \
  --mode managed --allow-managed-update

# Switch the normal Dashboard route to an isolated PR stack after it is ready.
odh-pr-deploy deploy --context CONTEXT --component gen-ai --pr PR_NUMBER \
  --mode live --allow-live-traffic

# For an existing Playground project, add the reversible OGX Responses API proxy.
odh-pr-deploy prepare-ogx --session SESSION_ID --project PROJECT_NAMESPACE

# Inspect and restore a session.
odh-pr-deploy status --session SESSION_ID
odh-pr-deploy cleanup --session SESSION_ID
odh-pr-deploy recover
```

`--image` accepts an explicit image reference instead of `--pr`. Every image is resolved to its registry digest before it is applied. The current version supports the `gen-ai` component; all `oc` calls require the supplied context. On this RHOAI installation, `gen-ai-ui` is owned by the Dashboard controller, so only durable `shadow` mode is permitted. This prevents a misleading transient update that the controller immediately reverts.

## Restoration guarantee

Before a deployment mutation, the tool saves a local session under `$ODH_PR_DEPLOY_STATE_DIR` or `$HOME/.local/state/odh-pr-deploy`. Live mode records the original route, Dashboard operator replica count, and any OGX ConfigMap value changed by `prepare-ogx`. `cleanup` refuses to overwrite values changed by another actor, restores the recorded values, and deletes only session-created resources. Controller-owned workloads are refused before mutation.

Shadow cleanup deletes only the tool-created Deployment and first confirms that the source managed deployment still has its original image.

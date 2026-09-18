# Contributing

This is a Crimson-maintained ODH Dashboard contributor utility. Changes must
preserve the narrow safety boundary: no user project data, OGX resources,
routes, unmanaged workloads, Secrets, or cluster RBAC may be changed.

## Development principles

- Treat the RHOAI operator CSV as the only supported image input. Never patch
  controller-owned workloads directly.
- Every cluster mutation needs a persisted inverse operation and a guarded
  cleanup path.
- Discovery must fail closed. Do not add guessed namespace, CSV, or image
  mappings for a new RHOAI topology.
- Keep the GenAI remote and Dashboard host compatible. A GenAI PR deployment
  is a paired stack transaction, not an independent container replacement.
- Never log or persist kubeconfigs, access tokens, provider keys, or Secret
  content.

Run before opening a pull request:

```bash
gofmt -w $(find ./cmd ./internal -name '*.go')
go test ./...
go test -race ./...
go vet ./...
```

Use a disposable cluster for manual testing. Record deploy and cleanup evidence in the pull request. Do not commit kubeconfigs, tokens, image-pull secrets, or session files.

For a behavior change, add a focused unit test using the command runner seam.
Test both the desired mutation and the guarded refusal/cleanup behavior. A live
cluster validation must finish with `cleanup --session` and confirmation that
both workloads returned to their recorded release images.

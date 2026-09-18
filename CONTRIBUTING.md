# Contributing

This is a Crimson-maintained ODH Dashboard contributor utility. Changes must preserve the narrow safety boundary: no user project data, OGX resources, routes, or unmanaged workloads may be changed.

Run before opening a pull request:

```bash
gofmt -w $(find ./cmd ./internal -name '*.go')
go test ./...
go test -race ./...
go vet ./...
```

Use a disposable cluster for manual testing. Record deploy and cleanup evidence in the pull request. Do not commit kubeconfigs, tokens, image-pull secrets, or session files.

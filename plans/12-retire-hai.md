# 12 — Retire HAI direct-mode

## Goal

Delete the CLI HAI direct-mode provider entirely. After this plan lands:

- `crabbox --provider tencent` only routes through the Worker (CVM).
- No code references `HAI`, `haiInstance`, `haiPrice`, `tencent-hai`,
  `TencentApplicationID`, `TencentBundleType`, `hai-` instance IDs, or
  `validateTencentHAISSHConfig`.
- The `tencent-hai` provider alias is removed.

## Sequencing

Land **after** both of these are true:

1. Milestone M5 in `plans/00-overview.md` is functional: Worker Tencent CVM
   can launch, list, terminate, and image a real instance.
2. That Worker version has been deployed to production (`crabbox.sh`) and the
   production readiness endpoint for `provider=tencent` returns the expected
   configured/missing-secret result.

The risk is deploy order, not merge order. If a CLI release deletes HAI before
production `crabbox.sh` runs the Tencent CVM Worker, upgraded users lose
`crabbox --provider tencent` even if the code exists on `main`. Do not tag the
HAI-retirement CLI release until the production Worker gate is checked.

## File-by-file deletions

### Pure deletes

- `internal/cli/tencent.go` — delete.
- `internal/cli/tencent_test.go` — delete.

### Edits

- `internal/cli/config.go`
  - Remove fields from `Config`:
    - `TencentApplicationID`
    - `TencentBundleType`
    - `TencentSystemDiskGB`
  - Keep `TencentSecretID`, `TencentSecretKey`, `TencentRegion`. These are
    reused by the Worker via env-var routing.
  - Remove constants `tencentHAIDefaultRegion`, `tencentHAIDefaultAppID`,
    `tencentHAIDefaultDiskGB`, `tencentHAIDefaultDiskType` from the package
    (they live in `tencent.go`; that file is deleted).
  - Remove the `defaults` lines that set
    `TencentApplicationID`/`TencentSystemDiskGB`.
  - Remove the YAML-file loader branches reading `Tencent.ApplicationID`,
    `Tencent.BundleType`, `Tencent.SystemDiskGB`.
  - Remove the env-var ingest lines for
    `CRABBOX_TENCENT_APPLICATION_ID`, `CRABBOX_TENCENT_BUNDLE_TYPE`,
    `CRABBOX_TENCENT_SYSTEM_DISK_GB`.
  - Drop the `tencent` branch of the helper that returns
    `TencentBundleType` as the default server type. After deletion, the
    helper falls through to the same default-class path as other providers.
- `internal/cli/config_cmd.go`
  - Remove the JSON output keys `applicationId`, `bundleType`,
    `systemDiskGB` from the `tencent` block.
  - Replace the `mode=hai` print with `mode=cvm` (or drop the mode field;
    aligning with how AWS/Azure print themselves is fine).
  - Delete `tencentConfigBundleType`.
- `internal/cli/doctor.go`
  - Remove the call to `validateTencentHAISSHConfig`.
  - Replace the `tencent` reachability check: instead of calling
    `newTencentClient` (which lived in the deleted file), hit the Worker
    readiness endpoint for provider `tencent`. Mirror the existing
    AWS/Azure pattern in this file.
- `internal/cli/providers_common.go:66`
  - Drop `|| strings.HasPrefix(server.CloudID, "hai-")`. CVM instance IDs
    start with `ins-`; that's a useful signal to keep on its own line.
- `internal/cli/run.go:1409`
  - Same edit — drop the `hai-` prefix branch. The `cfg.Provider ==
    "tencent" || server.Provider == "tencent"` check is enough.
- `internal/cli/provider_backend.go`
  - The provider list strings (`provider: hetzner, aws, azure, gcp,
    tencent, ...`) already say `tencent` — no change needed. Remove any
    fallback that mentions HAI by name in error messages, if any.
- `internal/cli/init.go`, `internal/cli/init_test.go`
  - Audit for HAI prompts / defaults; remove.
- `internal/cli/doctor_test.go`, `internal/cli/config_test.go`
  - Audit for HAI-shaped fixtures (`CRABBOX_SSH_KEY=~/.ssh/crabbox-hai`,
    etc.). Replace with neutral `crabbox` key paths.

### Provider registry

In `internal/cli/tencent.go` the file did:

```go
func (tencentProvider) Aliases() []string { return []string{"tencent-cloud", "tencent-hai"} }
```

After deleting that file, `tencent` would otherwise disappear from the
registry. Verified mechanism:

- `loadBackend` calls `ProviderFor(cfg.Provider)` before any coordinator
  routing. Unknown providers never reach the Worker path.
- `CoordinatorClient.CreateLease` also calls `ProviderFor` before posting the
  lease request.
- The coordinator wrapper is only applied when the configured provider returns
  an `SSHLeaseBackend`, `Spec().Coordinator == CoordinatorSupported`, and
  `cfg.Coordinator` is non-empty.
- `blacksmith-testbox` and `e2b` are not the model for this; they are
  registered delegated-run providers with `CoordinatorNever`, not
  Worker-backed cloud providers.

So HAI deletion must leave a registered Tencent provider stub, for example in
`internal/cli/tencent_coordinator.go`:

- `Name() == "tencent"`.
- `Aliases() == []string{"tencent-cloud"}`. Do **not** keep `tencent-hai`.
- `Spec()` uses `ProviderKindSSHLease`, `CoordinatorSupported`, and the
  target/features the Worker actually supports (Linux and native Windows;
  no macOS).
- `RegisterFlags` keeps only coordinator-relevant Tencent flags that still
  make sense (`--tencent-region`, secrets if config still exposes them); all
  HAI flags are gone.
- `Configure` returns a small `SSHLeaseBackend` whose methods fail clearly
  with `provider=tencent requires CRABBOX_COORDINATOR / broker.url` when it
  is not wrapped. When `cfg.Coordinator` is set, `loadBackend` wraps it in
  `coordinatorLeaseBackend`, and the direct methods are not used for normal
  lease acquisition.

Add regression tests in `internal/cli/provider_backend_test.go` and
`internal/cli/doctor_test.go`: `ProviderFor("tencent")` and
`ProviderFor("tencent-cloud")` succeed, `ProviderFor("tencent-hai")` fails,
`loadBackend` with coordinator returns `*coordinatorLeaseBackend`, and
readiness support is true for Tencent.

### Docs

- `docs/commands/run.md`, `docs/commands/warmup.md`, `docs/cli.md` — scan
  for HAI mentions. The current files list `tencent` in the provider
  union, which stays. Update any reference to HAI bundles / application
  IDs.
- `docs/features/` — if a `tencent-hai.md` or similar exists, delete it or
  rewrite as `tencent-cvm.md` (the new doc from `plans/10`).
- `CHANGELOG.md` — add an entry: `feat!: retire Tencent HAI direct-mode
  CLI provider; --provider tencent now routes through the Worker as CVM`.
  The `!` marks it as a breaking change.

### Config file migration

- Old `~/.config/crabbox/config.yaml` may have:
  ```yaml
  tencent:
    applicationId: app-...
    bundleType: ...
    systemDiskGB: 80
  ```
  These keys are silently ignored after this plan (the YAML loader drops
  unknown keys). Add a one-line log in `config.go` that warns when any of
  the three keys appears: "tencent.applicationId/bundleType/systemDiskGB
  ignored — HAI direct-mode retired; see docs/features/tencent-cvm.md".
  Delete the warning in a follow-up release.

## Acceptance

- `git grep -i 'hai' internal/ docs/ CHANGELOG.md` returns zero relevant
  hits (incidental matches inside vendored JS like `novnc/rfb.js` are
  fine).
- `go test ./...` passes.
- `crabbox --provider tencent doctor` works against a Worker that has
  CVM provider secrets set, and reports clear "missing secret" output
  when they're not set.
- `crabbox --provider tencent-hai run -- echo ok` now errors with
  "unknown provider 'tencent-hai'". Document the rename in the changelog.

## Risk and rollback

- Anyone with a `~/.config/crabbox/config.yaml` HAI block keeps working
  for the warning period because unknown YAML keys are ignored. They
  effectively switch to CVM-via-Worker without code change.
- Rollback path: revert this plan's commit. Worker CVM stays intact. The
  CLI regains HAI direct-mode. No state migration needed.

## Why retire HAI

For the record, so the rationale doesn't get lost:

- HAI uses **baked images only**: no cloud-init, no SSH key injection at
  launch. Crabbox's whole model (ephemeral, per-lease keys, user-data
  bootstrap) is hostile to that constraint.
- HAI's pricing API is opaque enough that the CLI carries a static price
  table as a fallback (`tencentStaticHourlyPriceCNY`). Maintenance burden.
- HAI's region/bundle vocabulary is product-specific (e.g., `app-...`,
  `BUNDLE_GPU_BASIC_*`). It doesn't compose with the rest of Crabbox's
  class/instance-type model.
- The HAI direct-mode path also needs separate validation (`validateTencentHAISSHConfig`)
  that nobody else needs.
- The owner has decided HAI is not worth the carry. Worker CVM replaces
  every workflow HAI was being used for.

# 10 — Tests and docs

## Tests (Vitest under `worker/test/`)

WebCrypto is available in the current Node Vitest setup (`worker/package.json`
requires Node >=22), and it is also available if tests later move to
`@cloudflare/vitest-pool-workers`. Production signing code should use
`globalThis.crypto.subtle`, not `node:crypto`.

### `tencent-signing.test.ts`

- One frozen vector: given `secretID=AKIDz8…`, `secretKey=Gu5t9xGA…`,
  `action=DescribeInstances`, `payload={"Limit":1,"Offset":0}`,
  `timestamp=1551113065`, assert the produced `Authorization` header
  matches the value Tencent's official docs publish. (See Tencent's
  TC3-HMAC-SHA256 signing example.)
- Second vector: cross-check against the Go implementation in
  `internal/cli/tencent.go` — generate a known-good signature with the Go
  helper, replay it in the TS test (hard-coded bytes), and assert equality.
- Payload-independence: changing `Content-Type` casing should not break.

### `tencent-candidates.test.ts`

- `tencentRegionCandidatesForTargetClass`: given a class and an env-provided
  region, the configured region appears first.
- `tencentInstanceTypeCandidatesForTargetClass`: assert order is stable for
  each `class`.
- `tencentZoneForRegion`: returns the configured zone when set, otherwise
  derives from `DescribeZones`. Mocked.

### `tencent-errors.test.ts`

- Table-driven: each error code maps to the expected category and retry
  flag. See `plans/06` for the table.

### `tencent-tags.test.ts`

- `tencentTagsFromLabels` then `tencentLabelsFromTags` is a near-identity
  modulo sanitization.
- Reserved prefixes (`tencent:`, `qcloud:`) are stripped on conversion.

### `tencent-launch.test.ts` (mocked HTTP)

- Mock `fetch` so that `RunInstances` returns a canned instance ID, then
  `DescribeInstances` returns a running instance with a public IP. Assert:
  - `RunInstances` payload contains the expected `ImageId`, `InstanceType`,
    tags, `LoginSettings.KeyIds`, `UserData` base64,
    `InternetAccessible.InternetMaxBandwidthOut > 0` when
    `PublicIpAssigned=true`, and an `InstanceName` no longer than 128 chars.
  - `createServerWithFallback` returns a `ProviderMachine` with
    `provider="tencent"`, `region`, `ip`.
- A second scenario where the first instance type returns
  `ResourceInsufficient.SpecifiedInstanceType`; assert the second
  candidate is attempted, and the failure is recorded as a
  `ProvisioningAttempt`.

### `fleet-tencent.test.ts`

See `plans/09`. Include the image/cache regression: a Tencent lease passed to
`POST /v1/images` dispatches to the Tencent provider stub rather than the AWS
provider, and `img-...` IDs pass validation.

## Docs

### New / updated pages

- `docs/features/tencent-cvm.md` (new) — overview of how the Worker provider
  is wired, env vars, region/instance type defaults, smoke-test recipe.
- `docs/commands/run.md` — add a `--provider tencent` example.
- `docs/commands/warmup.md` — add a Tencent CVM example.
- `docs/commands/usage.md` — note that Tencent pricing is in CNY at the
  source and converted to USD at a fixed rate set by
  `CRABBOX_TENCENT_CNY_USD_RATE`.
- `docs/commands/image.md` — note that custom images are supported on
  Tencent (same as AWS; unlike Azure).
- `docs/commands/azure.md` — no change. Tencent does not get a `tencent`
  subcommand namespace in this task.
- `docs/cli.md` — already lists `tencent` in `--provider` choices; verify
  no copy edits needed.

### CHANGELOG

Add a `feat:` entry: "worker: Tencent Cloud CVM provider (RunInstances,
network ensure, custom images, hourly price estimate)".

### PR description checklist

- [ ] manual smoke test: region, instance type, image, time-to-IP, cost.
- [ ] new env vars added to wrangler dashboard (operations doc).
- [ ] secrets list updated in `docs/features/tencent-cvm.md`.

## Acceptance

The CI gates listed in `TASK.md` plus the new Vitest suites green and the
docs site (`node scripts/build-docs-site.mjs`) renders without warnings.

# 09 — `fleet.ts` integration

## Goal

Wire `worker/src/tencent.ts` into the Worker the same way AWS / Azure / GCP /
Hetzner are wired. After this plan lands, every code path in `fleet.ts`
that branches on `provider` knows about `"tencent"`.

## Touchpoints in `worker/src/fleet.ts`

1. **`CloudProvider` adapter class**. Add `class TencentProvider implements
   CloudProvider` next to `AWSProvider`. Internally it owns a
   `TencentCVMClient`. Iterate over `tencentRegionCandidates` in
   `createServerWithFallback` when `config.tencentRegion` is unset,
   otherwise pin to one region (same pattern AWS uses).

2. **`provider(provider, region, project)` switch** (around fleet.ts:3229):
   ```ts
   if (provider === "tencent") {
     return new TencentProvider(this.env, region || this.env.CRABBOX_TENCENT_REGION || "ap-singapore");
   }
   ```

3. **`deleteLeaseServer(lease)`** (around fleet.ts:3247):
   ```ts
   if (lease.provider === "tencent") {
     await this.provider("tencent", lease.region).deleteServer(lease.cloudID);
     if (validTencentProviderKey(lease.providerKey)) {
       await this.provider("tencent", lease.region).deleteSSHKey(lease.providerKey);
     }
     return;
   }
   ```

4. **Pool/list cross-provider sweep** (around fleet.ts:2492-2504 and
   `listProviderMachinesSafe`): add `"tencent"` to the providers iterated
   when a request asks for "everything", and add an explicit
   `provider === "tencent"` branch so `/v1/pool?provider=tencent` does not
   fall through to the all-provider path.

5. **`providerRequiredSecrets`** (around fleet.ts:3324-3338):
   ```ts
   case "tencent":
     return ["TENCENT_SECRET_ID", "TENCENT_SECRET_KEY"];
   ```

6. **`isManagedProvider`** (around fleet.ts:3342):
   ```ts
   return provider === "aws" || provider === "azure" || provider === "gcp" ||
          provider === "hetzner" || provider === "tencent";
   ```

7. **Config seeding inside the request handler** (around fleet.ts:816-826):
   ```ts
   if (config.provider === "tencent" && !config.tencentRegion) {
     config.tencentRegion = this.env.CRABBOX_TENCENT_REGION || "ap-singapore";
   }
   if (config.provider === "tencent" && config.tencentSSHCIDRs.length === 0) {
     config.tencentSSHCIDRs = requestSourceCIDRs(request);
   }
   ```

8. **Record decoration** when a lease is persisted (around fleet.ts:963-975):
   ```ts
   if (config.provider === "tencent") {
     record.region = server.region ?? config.tencentRegion;
   }
   ```

9. **Image/cache dispatch** (around fleet.ts:2909-2960): the current path is
   AWS-hardcoded. Update `createImage` to accept Tencent leases and call
   `this.provider(lease.provider, lease.region).createImage(...)`; update
   `validImageID` to accept Tencent `img-...`; and make `imageRoute` choose
   the provider from route/query/body/stored metadata instead of always
   calling `this.provider("aws")`. Keep unsupported-provider errors for
   providers whose adapters still throw.

10. **Server-shaped routing** in `validateRequest`-style helpers
    (fleet.ts:4137-4140): add `"tencent"` to the union check.

## Touchpoints elsewhere

- `worker/src/index.ts`: no changes expected; dispatching is purely by
  pathname, not provider.
- `worker/src/provider-labels.ts`: no changes; the `provider` argument is
  already free-form.
- `worker/src/auth.ts`: no changes; auth happens before provider routing.

## Test surface

- A new `worker/test/fleet-tencent.test.ts` that injects a stub
  `TencentProvider` via the `testProviders` constructor argument (already
  supported on line 259) and verifies:
  - `/v1/lease` with `provider: "tencent"` hits the stub.
  - `/v1/lease/<id>/release` deletes the server.
  - `/v1/list` includes Tencent machines.
  - `/v1/images` for a Tencent lease dispatches to the Tencent stub, accepts
    a `img-...` ID, and never touches the AWS provider.
  - Readiness endpoint returns the correct `missing` list when secrets are
    unset.

## Acceptance

- After this plan lands, a CLI run of `crabbox warmup --provider tencent`
  reaches `RunInstances` (and fails or succeeds based on real Tencent
  credentials, not because of plumbing).
- Worker compiles, lints, and the new Vitest suite passes.

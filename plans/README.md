# Plans

Implementation plans for the active focus described in [`../TASK.md`](../TASK.md).

Each plan is scoped, names the Tencent SDK actions involved, and cites the
AWS/Azure code it mirrors. Treat plans as living documents — update them as
decisions firm up. When a plan lands, mark it `done` in `TASK.md` and leave
the file as a record of the design.

## Reading order

1. [00 Overview](00-overview.md) — capability matrix, command → API mapping, milestones.
2. [01 Signing & HTTP](01-signing-and-http.md) — TC3-HMAC-SHA256 in WebCrypto, JSON Action API client.
3. [02 Config & types](02-config-and-types.md) — `Provider` union, `LeaseConfig` fields, env vars, secrets.
4. [03 Launch (RunInstances)](03-launch-runinstances.md) — image / type / zone resolution, fallback ordering.
5. [04 Network (VPC / Subnet / SG)](04-network-vpc-sg.md) — ensure shared infra, SSH CIDRs.
6. [05 Key pair & user-data](05-keypair-and-userdata.md) — `ImportKeyPair`, cloud-init, Windows bootstrap.
7. [06 Lifecycle & list](06-lifecycle-and-list.md) — list / get / delete, start / stop / reboot, tags ↔ labels, error classification.
8. [07 Images & cache](07-images-and-cache.md) — `CreateImage` / `DescribeImages` / `DeleteImages`, `crabbox image` & `crabbox cache`.
9. [08 Pricing, quota, spot](08-pricing-quota-fallback.md) — `InquiryPriceRunInstances`, `DescribeAccountQuota`, `SPOTPAID`.
10. [09 Fleet integration](09-fleet-integration.md) — `CloudProvider` impl, `provider()` switch, `providerRequiredSecrets`.
11. [10 Tests & docs](10-tests-and-docs.md) — Vitest fixtures, signing parity tests, docs updates.
12. [11 Stretch capabilities](11-stretch-vnc-rescue.md) — VNC URL, password reset, rescue mode, TAT, disk resize.
13. [12 Retire HAI](12-retire-hai.md) — delete the CLI HAI direct-mode provider; gated on Worker CVM being functional and deployed to production.

## Conventions

- Split layout (see [`00-overview.md` → File layout](00-overview.md#file-layout)):
  `tencent.ts`, `tencent-signing.ts`, `tencent-network.ts`,
  `tencent-pricing.ts`, `tencent-types.ts`. `fleet.ts` only imports from
  `tencent.ts`.
- Mirror AWS shape at the boundary: a single facade class
  (`TencentCVMClient`, analogous to `EC2SpotClient`) + a `TencentProvider`
  adapter inside `fleet.ts`.
- Naming: `tencentLaunchCandidates`, `tencentRegionCandidates`,
  `tencentZoneForRegion`, `isRetryableTencentProvisioningError`,
  `tencentTagsFromLabels`, `tencentLabelsFromTags`,
  `tencentProvisioningErrorCategory`, `validTencentProviderKey`,
  `applyTencentRunInstanceTargetOptions`.
- Secrets: `TENCENT_SECRET_ID`, `TENCENT_SECRET_KEY`, optional
  `CRABBOX_TENCENT_REGION`. Never accept secrets via query string or path.
- Reference, not import: the existing Go code in `internal/cli/tencent.go`
  is for the HAI product and is being retired (see
  [plan 12](12-retire-hai.md)). Use it as a TC3 signing parity reference
  until it's deleted; do not port behavior verbatim.
- Reference, not import: the vendored Go SDK at `../tencentcloud-sdk-go/` is
  for API shapes and field names. The Worker speaks the JSON Action API
  directly; do not bundle Go.

## Out of scope

- HAI anywhere. The product is being retired in this task; see
  [plan 12](12-retire-hai.md).
- CLI direct-mode for Tencent CVM. `--provider tencent` from the CLI only
  routes through the Worker after HAI is gone. A CLI-direct CVM backend is
  a follow-up if someone asks for it.
- Cross-provider refactors. AWS/Azure stay untouched except for trivial
  edits to extension points (`Provider` union, `providerRequiredSecrets`,
  etc.).

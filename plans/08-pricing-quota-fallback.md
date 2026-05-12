# 08 — Pricing, quota, spot

## Goal

Implement `hourlyPriceUSD` and quota preflight so:

- `crabbox usage` can report an hourly estimate for Tencent-launched leases.
- The launch path can skip instance types whose quota is exhausted instead
  of bouncing off the API for every attempt.
- SPOTPAID (Tencent's spot equivalent) is an opt-in market for cost-savvy
  workloads.

Mirror: `EC2SpotClient.hourlySpotPriceUSD`, `awsQuotaPreflightAttempt`,
the `awsSpotQuotaCode` / `awsOnDemandQuotaCode` constants, and
`awsQuotaCodeForMarket` in `worker/src/aws.ts`.

Lives in `worker/src/tencent-pricing.ts`. Pure functions only; the file
takes a `tencentCall` reference (or env+signing module) as input so it can
be tested without HTTP.

## Pricing

### Hourly price for a resolved launch config

`tencentHourlyPriceUSD({ serverType, config, zone, chargeType, imageId })`:

1. Require a resolved `imageId` from the launch path (`resolveImage`) or an
   explicit `config.tencentImage` / `CRABBOX_TENCENT_IMAGE`. `ImageId` is a
   required field in the SDK's `InquiryPriceRunInstancesRequest`; pricing must
   not call `DescribeImages` itself and must not fall back to a fake public
   image ID.
2. Call `InquiryPriceRunInstances` with the same payload as the actual launch
   would use (instance type, zone, charge type, system disk, internet
   bandwidth, resolved image ID).
3. Tencent returns `Price.InstancePrice.UnitPriceDiscount` in **CNY per hour**.
4. Convert to USD using a static rate (env-overridable
   `CRABBOX_TENCENT_CNY_USD_RATE`, default `0.14`). Mark imprecise via a
   `priceSource: "estimated"` field where applicable.

The `CloudProvider.hourlyPriceUSD(serverType, config)` adapter keeps the
existing fleet interface: use live pricing only when `config.tencentImage`
already contains a real resolved ID (for example after launch resolution),
otherwise return the static fallback estimate or `undefined`. Do not hide an
image resolver inside pricing.

### Static fallback table

If `InquiryPriceRunInstances` fails (auth, network, or unsupported zone),
fall back to a small static table keyed on instance type family. Capture
the shape of the CLI's `tencentStaticHourlyPriceCNY` (HAI) before plan 12
deletes it; replace with CVM-typed entries. Start with a few common
families:

```ts
{ "S5.SMALL2": 0.10, "S5.MEDIUM4": 0.20, "S5.LARGE8": 0.40,
  "SA3.MEDIUM4": 0.18, "SA3.LARGE16": 0.80, ... } // CNY/hour
```

Document this is a coarse estimate. The real path is `InquiryPriceRunInstances`.

## Quota preflight

`tencentQuotaPreflightAttempt(client, config, instanceType, market) →
ProvisioningAttempt | null`:

1. Call `DescribeAccountQuota` with `AccountQuotaType=PostPaidQuotaSet` or
   `SpotPaidQuotaSet`.
2. Tencent returns quota objects per region; find the one matching
   `config.tencentRegion` and `instanceFamily(instanceType)`.
3. If `UsedQuota >= TotalQuota`, return a `ProvisioningAttempt` with
   `category: "quota"` and a clear message ("tencent quota exhausted for
   <family> in <region>: <used>/<total>"). Caller treats it like AWS
   treats a quota miss: skip to the next instance type without burning a
   RunInstances call.
4. If quota lookup fails entirely, return `null` (don't block the launch).

## SPOTPAID (spot)

Tencent's spot equivalent is `InstanceChargeType=SPOTPAID` with a price cap:

```json
"InstanceMarketOptions": {
  "MarketType": "spot",
  "SpotOptions": {
    "MaxPrice": "0.50",      // optional; omit for current spot price
    "SpotInstanceType": "one-time"
  }
}
```

Behavior parity with AWS:

- `config.capacityMarket === "on-demand"` → `POSTPAID_BY_HOUR`.
- `config.capacityMarket === "spot"` (or any non-`on-demand`) → SPOTPAID
  with the price cap fetched from `InquiryPriceRunInstances` using the same
  resolved `imageId` as launch (current price × `1.5`, capped at the
  on-demand price).
- Same retryable-error rules as AWS: `ResourceInsufficient.*` triggers the
  next instance type / zone.

## Acceptance

- `crabbox usage --provider tencent --class beast` returns a number, with a
  `priceSource` field marking it `live` or `estimated`.
- Launching with `--market spot` on a Tencent account where the family has
  no spot capacity falls through to on-demand in the same region without
  user intervention.
- Launching with quota exhausted in region A successfully falls to region
  B with one `quota` attempt recorded in the lease record.

## Open questions

- Is SPOTPAID worth the test-time cost? **Yes** for cost; **risky** for
  flaky CI. Default to `on-demand` unless the user opts in via `--market
  spot`. AWS already behaves the same way.
- Static CNY rate: cheap and obviously wrong sometimes. Acceptable for
  a usage estimate; document the limitation in `docs/commands/usage.md`.

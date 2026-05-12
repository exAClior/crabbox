# 02 — Worker config, types, and secrets

## Goal

Extend the Worker's `Provider` union and `LeaseConfig` to carry Tencent CVM
options, and add Tencent secrets to the env surface. No behavior change yet.

## Changes

### `worker/src/types.ts`

- Extend the `Provider` union:
  ```ts
  export type Provider = "hetzner" | "aws" | "azure" | "gcp" | "tencent";
  ```
- Extend `LeaseConfig` with Tencent fields (mirror the AWS/Azure block
  style):
  ```ts
  tencentRegion?: string;          // e.g. "ap-singapore"
  tencentZone?: string;            // e.g. "ap-singapore-1"
  tencentImage?: string;           // img-...; resolved if blank
  tencentVPCID?: string;
  tencentSubnetID?: string;
  tencentSecurityGroupID?: string;
  tencentSSHCIDRs?: string[];
  tencentSystemDiskGB?: number;
  tencentSystemDiskType?: string;  // CLOUD_PREMIUM | CLOUD_SSD | CLOUD_BSSD
  tencentInternetMaxBandwidthOutMbps?: number; // default 5 when public IP is requested
  tencentInstanceChargeType?: "POSTPAID_BY_HOUR" | "SPOTPAID";
  ```
- Extend `Env`:
  ```ts
  TENCENT_SECRET_ID?: string;
  TENCENT_SECRET_KEY?: string;
  CRABBOX_TENCENT_REGION?: string;
  CRABBOX_TENCENT_IMAGE?: string;
  CRABBOX_TENCENT_VPC_ID?: string;
  CRABBOX_TENCENT_SUBNET_ID?: string;
  CRABBOX_TENCENT_SECURITY_GROUP_ID?: string;
  CRABBOX_TENCENT_SYSTEM_DISK_GB?: string;
  CRABBOX_TENCENT_INTERNET_BANDWIDTH_MBPS?: string;
  ```

### `worker/src/config.ts`

- Hook the new fields into `leaseConfig(...)` with defaults that fall back
  to env. Mirror `awsRegion` / `azureLocation` plumbing.
- Default `tencentInternetMaxBandwidthOutMbps` to `5` (or
  `CRABBOX_TENCENT_INTERNET_BANDWIDTH_MBPS` when set). If the launch payload
  sets `PublicIpAssigned: true`, validate the final bandwidth is `> 0` before
  calling Tencent; CVM's SDK documents `0Mbps` as the default and disallows
  public IP assignment at zero bandwidth.
- Add helpers:
  - `tencentRegionCandidatesForTargetClass(class)` — small static map to
    start; can grow.
  - `tencentInstanceTypeCandidatesForTargetClass(class)` — analogous to
    `awsInstanceTypeCandidatesForTargetClass`. Use Tencent's family naming
    (`S5.MEDIUM4`, `SA3.LARGE16`, etc.).
  - `tencentSystemDiskTypeFor(region)` — default `CLOUD_PREMIUM`, override
    via env.

### `worker/src/auth.ts`, `worker/src/fleet.ts`

- Add `"tencent"` to the `providerRequiredSecrets` switch:
  ```ts
  case "tencent":
    return ["TENCENT_SECRET_ID", "TENCENT_SECRET_KEY"];
  ```
- Add `"tencent"` to `isManagedProvider` and the providers iterated in
  `listProviderMachinesSafe`.

## Reference

- AWS shape: `worker/src/types.ts` lines around `awsRegion`/`awsAMI`.
- Azure shape: `azureLocation`/`azureImage` in the same file.
- CLI defaults: `internal/cli/config.go` already has `TencentRegion`,
  `TencentSecretID`, `TencentSecretKey`. Worker keeps the **same env var
  names** so a user with `TENCENT_SECRET_ID` set in their wrangler `vars`
  needs no extra plumbing.

## Non-changes

- No new YAML keys in the CLI side. The CLI surface (`internal/cli/config.go`)
  already exposes a Tencent block. CLI CVM mode (if ever added) would land
  in a follow-up.
- No change to the on-disk Durable Object schema. Tencent fields are
  optional, so leases created before this lands still load.

## Acceptance

- `npm run check --prefix worker` is green.
- `crabbox doctor --provider tencent` (CLI) talks to the Worker readiness
  endpoint and reports either "configured" or a clean `missing:
  TENCENT_SECRET_ID, TENCENT_SECRET_KEY` message.

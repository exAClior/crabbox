# 00 — Overview: Tencent CVM in the Worker

## Why

Crabbox already supports AWS, Azure, GCP, and Hetzner inside the Worker. The
CLI has carried a direct-mode **Tencent HAI** provider for a while, but HAI's
baked-image / no-cloud-init constraints make it a poor fit for Crabbox's
workflow. We're retiring HAI entirely (see [plan 12](12-retire-hai.md)) and
standing up **Tencent Cloud CVM** as a first-class Worker provider, so any
`crabbox` command that targets a managed cloud provider can also use Tencent.

## File layout

Unlike `worker/src/aws.ts` (~875 lines, single file) and `worker/src/azure.ts`
(~830 lines, single file), Tencent CVM is split across **five files** so that
signing, networking, pricing, and core launch logic can be edited in
isolation. Total LOC is comparable; the slices are along axes that change
for independent reasons.

| File                                 | Role                                                                                | Approx LOC |
| ------------------------------------ | ----------------------------------------------------------------------------------- | ---------- |
| `worker/src/tencent.ts`              | `TencentCVMClient` facade + `TencentProvider` adapter; re-exports public surface    | ~400       |
| `worker/src/tencent-signing.ts`      | TC3-HMAC-SHA256, `tencentCall<T>`, `TencentAPIError`                                | ~150       |
| `worker/src/tencent-network.ts`      | VPC/Subnet/SG ensure paths (calls `vpc.tencentcloudapi.com`)                        | ~250       |
| `worker/src/tencent-pricing.ts`      | `InquiryPriceRunInstances`, quota preflight, SPOTPAID helpers, CNY → USD            | ~200       |
| `worker/src/tencent-types.ts`        | Shared request/response shapes; instance/image/key-pair/tag interfaces              | ~100       |

`fleet.ts` imports only from `worker/src/tencent.ts`. Tests under
`worker/test/` import from the specific module they exercise (e.g. signing
tests import from `tencent-signing.ts`).

If the codebase later decides to flatten back to one file, this layout makes
the merge mechanical — each module has clear responsibilities and no cyclic
imports.

## What "parity" means here

Concretely: the new module implements the existing `CloudProvider` interface
in `worker/src/fleet.ts`:

```ts
interface CloudProvider {
  listCrabboxServers(): Promise<ProviderMachine[]>;
  createServerWithFallback(config, leaseID, slug, owner): Promise<{ server, serverType, market?, attempts? }>;
  deleteServer(id: string): Promise<void>;
  createImage(instanceID, name, noReboot): Promise<ProviderImage>;
  getImage(imageID: string): Promise<ProviderImage>;
  deleteSSHKey(name: string): Promise<void>;
  hourlyPriceUSD(serverType, config): Promise<number | undefined>;
}
```

…plus the surrounding wiring (`providerRequiredSecrets`,
`deleteLeaseServer`, `listProviderMachinesSafe`, `isManagedProvider`,
`providerReadiness`) for `provider: "tencent"`.

## User-facing command coverage

From `docs/commands/`, the commands that touch the provider directly and
will exercise this code path:

| Command       | What it needs from the provider                                                       | Tencent SDK actions                                                                              |
| ------------- | ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `doctor`      | Reachability + auth smoke test                                                        | `DescribeInstances` (limit 1)                                                                    |
| `warmup`      | Full launch: VPC/subnet/SG, key, image resolve, user-data, RunInstances, wait for IP  | `Run/DescribeInstances`, `vpc:CreateVpc/Subnet/SecurityGroup*`, `ImportKeyPair`, `DescribeImages`|
| `run` / `job` | Same launch (when `--id` is fresh), then SSH path is provider-agnostic                | same as warmup                                                                                   |
| `desktop`     | Same launch with `target=linux,desktop=true` flags; bootstrap installs VNC server     | same as warmup, plus `DescribeInstanceVncUrl` (stretch)                                          |
| `ssh`         | Active lease's public IP and provider key                                             | `DescribeInstances`                                                                              |
| `list`        | List all crabbox instances by tag                                                     | `DescribeInstances` with `tag:crabbox=true`                                                      |
| `status` / `inspect` | Read instance attributes by ID                                                 | `DescribeInstances` / `DescribeInstancesAttributes`                                              |
| `stop`        | Stop or terminate the instance backing a lease                                        | `TerminateInstances`, optionally `StopInstances`                                                 |
| `cleanup`     | Bulk delete by tag                                                                    | `DescribeInstances` + `TerminateInstances`                                                       |
| `cache`       | Snapshot a warmed image for reuse                                                     | `CreateImage`, `DescribeImages`                                                                  |
| `image`       | List / create / delete custom images                                                  | `CreateImage`, `DescribeImages`, `DeleteImages`, `ModifyImageAttribute`                          |
| `usage`       | Hourly price estimate for the chosen instance type                                    | `InquiryPriceRunInstances` (or static table fallback)                                            |
| `vnc`/`webvnc`| (Stretch) launch native CVM VNC                                                       | `DescribeInstanceVncUrl`                                                                         |
| `media`/`screenshot`/`code`/`egress`/`attach`/`logs`/`events`/`results` | nothing from the cloud provider beyond an active instance — provider-agnostic | n/a |

`actions`, `admin`, `share`/`unshare`, `artifacts`, `history`, `sync-plan`,
`init`/`login`/`logout`/`whoami`, `config` are coordinator-side and require
no provider work. The existing Tencent secrets (`TencentSecretID`,
`TencentSecretKey`, `TencentRegion`) in `internal/cli/config.go` stay; HAI-only
fields (`TencentApplicationID`, `TencentBundleType`, `TencentSystemDiskGB`)
are deleted as part of [plan 12](12-retire-hai.md).

## Milestones

A suggested chunking of the plans into landing-sized PRs. Pick one per PR.

1. **M0 — Skeleton** — `worker/src/tencent.ts` facade + `tencent-signing.ts`
   + `tencent-types.ts`. Stub `TencentCVMClient`, TC3 signing,
   `DescribeInstances` happy path, wired into `fleet.ts` enough to show up
   in readiness checks. (`plans/01`, `plans/02`, `plans/09` partial)
2. **M1 — Launch** — `RunInstances` happy path with hardcoded VPC/subnet/SG
   IDs from env. No fallback. Verifies signing + user-data + tagging end to
   end. (`plans/03`, `plans/05`)
3. **M2 — Ensure infra** — `tencent-network.ts`: create VPC, subnet, SG,
   key pair if missing. (`plans/04`)
4. **M3 — Fallback** — `tencent-pricing.ts` + launch fallback: zone +
   instance type candidates, retryable error classification, optional
   SPOTPAID. (`plans/03`, `plans/08`)
5. **M4 — Lifecycle + list** — delete, list by tag, start/stop/reboot, label
   round-trip. (`plans/06`)
6. **M5 — Images** — `createImage`/`getImage` so `crabbox cache` and
   `crabbox image` work against Tencent. (`plans/07`)
7. **M6 — Tests + docs** — Vitest, signing parity test against the Go HAI
   implementation (captured before deletion), `docs/commands/*` updates.
   (`plans/10`)
8. **M7 — Retire HAI** — delete `internal/cli/tencent.go`, drop HAI config
   fields, scrub `hai-` cloudID branches, update docs. Lands **after** M5 is
   functional **and** the Tencent CVM Worker has been deployed to production
   (`crabbox.sh`), so upgraded CLI users keep `--provider tencent` working
   through the Worker without a gap. (`plans/12`)
9. **M8 — Stretch** — VNC URL, password reset, rescue, TAT-driven Windows
   bootstrap, disk resize. (`plans/11`)

M0–M5 deliver the primary acceptance criterion in `TASK.md`. M6 is required
for merge. M7 is gated on the production Worker deploy, not just code merge.
M8 is post-merge nice-to-haves.

## Key references

- Tencent CVM SDK (vendored): `../tencentcloud-sdk-go/tencentcloud/cvm/v20170312/`
- Tencent VPC SDK (vendored): `../tencentcloud-sdk-go/tencentcloud/vpc/v20170312/`
- Tencent API portal: https://www.tencentcloud.com/document/api/213
- AWS reference module: `worker/src/aws.ts`
- Azure reference module: `worker/src/azure.ts`
- CLI HAI (being retired; see [plan 12](12-retire-hai.md)): `internal/cli/tencent.go`. Use as a TC3 signing reference until it's deleted.
- Worker provider interface + dispatch: `worker/src/fleet.ts` (search
  `interface CloudProvider`).

## Non-goals (re-stated)

- HAI in any form. The product is being retired in this task; see [plan
  12](12-retire-hai.md).
- CLI direct-mode for Tencent CVM. After HAI is gone, CLI `--provider
  tencent` only routes through the Worker. A CLI-direct CVM backend (analog
  to `internal/cli/aws.go` / `internal/cli/azure.go`) is a follow-up if
  someone needs it.
- Re-architecting the `CloudProvider` interface for any provider other than
  Tencent. If a method's signature feels wrong, file an issue and keep the
  existing shape for this task.

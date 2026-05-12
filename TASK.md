# Current Focus

> Single source of truth for what the repo is actively working on. Update on
> any direction change. When the task is done, move this file to
> `plans/archive/` rather than leaving stale goals here.

## Goal (one line)

Bring **Tencent Cloud CVM** support in the Worker to functional parity with
the existing **AWS** and **Azure** providers — enough that
`crabbox warmup --provider tencent` and `crabbox run --provider tencent` work
end-to-end against a real Tencent account.

## Status

| Workstream                  | Plan                                                                          | State |
| --------------------------- | ----------------------------------------------------------------------------- | ----- |
| Overview & capability map   | [plans/00-overview.md](plans/00-overview.md)                                  | in-progress |
| TC3 signing + HTTP client   | [plans/01-signing-and-http.md](plans/01-signing-and-http.md)                  | done |
| Worker config & types       | [plans/02-config-and-types.md](plans/02-config-and-types.md)                  | done |
| Launch path (RunInstances)  | [plans/03-launch-runinstances.md](plans/03-launch-runinstances.md)            | done |
| Network (VPC / subnet / SG) | [plans/04-network-vpc-sg.md](plans/04-network-vpc-sg.md)                      | done |
| Key pair + user-data        | [plans/05-keypair-and-userdata.md](plans/05-keypair-and-userdata.md)          | done |
| Lifecycle + list + tags     | [plans/06-lifecycle-and-list.md](plans/06-lifecycle-and-list.md)              | done |
| Images + `cache` / `image`  | [plans/07-images-and-cache.md](plans/07-images-and-cache.md)                  | done |
| Pricing / quota / SPOTPAID  | [plans/08-pricing-quota-fallback.md](plans/08-pricing-quota-fallback.md)      | done |
| `fleet.ts` integration      | [plans/09-fleet-integration.md](plans/09-fleet-integration.md)                | done |
| Tests + docs                | [plans/10-tests-and-docs.md](plans/10-tests-and-docs.md)                      | in-progress |
| Stretch (VNC, rescue, TAT)  | [plans/11-stretch-vnc-rescue.md](plans/11-stretch-vnc-rescue.md)              | draft |
| Retire HAI direct-mode      | [plans/12-retire-hai.md](plans/12-retire-hai.md)                              | draft |

Update the state column (`draft` → `in-progress` → `done`) as work lands.

## Acceptance (gate before PR)

Primary acceptance: from the Worker, launch a specific Tencent CVM instance
end-to-end. The instance must appear in the fleet view with correct provider
labels and be terminable through the same code path. Plus the worker CI gates:

- `npm run format:check --prefix worker` clean
- `npm run lint --prefix worker` clean
- `npm run check --prefix worker` clean (TypeScript)
- `npm test --prefix worker` passes, including new Tencent tests
- `npm run build --prefix worker` (Wrangler dry-run) succeeds
- A manual smoke test against a real Tencent account documented in the PR
  description (region, instance type, image, result), secrets redacted

## Scope summary

- **In scope:** `worker/` — five new files under `worker/src/tencent*.ts`
  (see file-split decision in [plans/00](plans/00-overview.md#file-layout)),
  wiring into `fleet.ts` / `index.ts` / `types.ts` / `provider-labels.ts`,
  plus tests under `worker/test/`. Worker docs and `docs/commands/*` updates
  where user-visible behavior shifts.
- **In scope (CLI):** Retire the HAI direct-mode provider. `internal/cli/tencent.go`,
  `internal/cli/tencent_test.go`, HAI-specific config fields
  (`TencentApplicationID`, `TencentBundleType`), the `tencent-hai` alias,
  and `hai-` cloudID branches in `run.go`/`providers_common.go` all go
  away. Plan: [plans/12-retire-hai.md](plans/12-retire-hai.md).
- **Out of scope:** CLI direct-mode for Tencent CVM. After HAI is retired,
  CLI `--provider tencent` only routes through the Worker. If a CLI-direct
  CVM backend is wanted later, it's a follow-up plan.
- **Sequencing:** HAI retirement lands **after** the Worker CVM provider
  is functional (Milestone M5 done) **and deployed to production**
  (`crabbox.sh`), so `--provider tencent` keeps working through the
  transition. See `plans/00-overview.md` → Milestones.

## Capability matrix (progressive disclosure)

A single-line summary; full mapping lives in
[plans/00-overview.md](plans/00-overview.md).

| Capability surface                     | AWS today                                      | Azure today                                | Tencent CVM plan                                                |
| -------------------------------------- | ---------------------------------------------- | ------------------------------------------ | --------------------------------------------------------------- |
| Auth                                   | SigV4 via `aws4fetch`                          | OAuth2 client credentials → bearer         | TC3-HMAC-SHA256 via WebCrypto (`plans/01`)                      |
| Launch                                 | `RunInstances` + spot fallback                 | ARM compose VM + NIC + IP + NSG            | `RunInstances` + zone/type fallback (`plans/03`)                |
| Network                                | `ensureSecurityGroup` + describeVPC            | `ensureSharedInfra` (VNet/Subnet/NSG)      | `vpc:CreateVpc/Subnet/SecurityGroup*` (`plans/04`)              |
| Key pair                               | `ImportKeyPair` + `DescribeKeyPairs`           | inline `osProfile.linuxConfiguration`      | `cvm:ImportKeyPair/AssociateInstancesKeyPairs` (`plans/05`)     |
| User-data                              | base64 cloud-init via `UserData`               | `osProfile.customData`                     | base64 cloud-init via `UserData` (`plans/05`)                   |
| List / describe                        | `DescribeInstances` + tag filter               | `Microsoft.Compute/virtualMachines list`   | `DescribeInstances` + tag filter (`plans/06`)                   |
| Delete                                 | `TerminateInstances`                           | LRO delete VM + dependent resources        | `TerminateInstances` (`plans/06`)                               |
| Lifecycle (start/stop/reboot)          | `StartInstances`/`StopInstances`/`Reboot…`     | `start`/`deallocate`/`restart`             | `StartInstances`/`StopInstances`/`RebootInstances` (`plans/06`) |
| Tags ↔ labels                          | `TagSpecification.*` flat                      | `azureTagsFromLabels` / `…FromTags`        | `Tags.N.{Key,Value}` (`plans/06`)                               |
| Custom image / cache                   | `CreateImage` / `DescribeImages`               | not supported (throws)                     | `CreateImage` / `DescribeImages` (`plans/07`)                   |
| Pricing / quota                        | `DescribeSpotPrice…` + Service Quotas          | not surfaced                               | `InquiryPriceRunInstances` + `DescribeAccountQuota` (`plans/08`)|
| Spot                                   | `InstanceMarketOptions.MarketType=spot`        | not supported                              | `InstanceChargeType=SPOTPAID` (`plans/08`)                      |
| Region/zone candidates                 | `awsRegionCandidates` + AZ helper              | `azureLocationFor`                         | `tencentRegionCandidates` + `DescribeZones` (`plans/03`,`08`)   |
| VNC                                    | not supported                                  | not supported                              | `DescribeInstanceVncUrl` (stretch — `plans/11`)                 |
| Password reset                         | not supported                                  | via custom extension                       | `ResetInstancesPassword` (stretch — `plans/11`)                 |
| Rescue mode                            | not supported                                  | not supported                              | `EnterRescueMode/ExitRescueMode` (stretch — `plans/11`)         |

## Plans folder

All implementation notes live in [`plans/`](plans/README.md). One file per
workstream, each ~half-page to a page, with concrete Tencent SDK actions and
the AWS/Azure functions it mirrors. Start with `plans/00-overview.md`.

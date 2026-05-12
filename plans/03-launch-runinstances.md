# 03 — Launch path: `RunInstances`

## Goal

`TencentCVMClient.createServerWithFallback(config, leaseID, slug, owner)` launches a
Tencent CVM instance with the right image, instance type, network, SSH key,
user-data, and tags — and returns a `ProviderMachine` that has a public IP.

Mirror: `EC2SpotClient.createServerWithFallback` and `createServer` in
`worker/src/aws.ts`.

## Tencent API actions

| Step                                   | Action                              |
| -------------------------------------- | ----------------------------------- |
| Resolve a public Ubuntu image          | `DescribeImages`                     |
| Resolve / validate an instance type    | `DescribeInstanceTypeConfigs`, `DescribeZoneInstanceConfigInfos` |
| Launch                                 | `RunInstances`                      |
| Wait for public IP / running state     | `DescribeInstances` (poll)          |

## RunInstances payload skeleton

```json
{
  "InstanceChargeType": "POSTPAID_BY_HOUR",
  "Placement": {
    "Zone": "ap-singapore-1",
    "ProjectId": 0
  },
  "InstanceType": "S5.MEDIUM4",
  "ImageId": "img-xxxxxxxx",
  "SystemDisk": { "DiskType": "CLOUD_PREMIUM", "DiskSize": 80 },
  "VirtualPrivateCloud": {
    "VpcId": "vpc-xxxx",
    "SubnetId": "subnet-xxxx"
  },
  "InternetAccessible": {
    "InternetChargeType": "TRAFFIC_POSTPAID_BY_HOUR",
    "InternetMaxBandwidthOut": 5,
    "PublicIpAssigned": true
  },
  "InstanceCount": 1,
  "InstanceName": "crabbox-<lease>-<slug>",
  "LoginSettings": { "KeyIds": ["skey-..."] },
  "SecurityGroupIds": ["sg-..."],
  "UserData": "<base64 cloud-init>",
  "ClientToken": "<leaseID>",
  "TagSpecification": [
    {
      "ResourceType": "instance",
      "Tags": [
        { "Key": "crabbox", "Value": "true" },
        { "Key": "lease",   "Value": "<leaseID>" },
        ...
      ]
    }
  ]
}
```

`ClientToken = leaseID` gives us free idempotency. Reuse from AWS pattern.

Two Tencent-specific guardrails belong in the payload builder, not in a later
poll loop:

- `InternetAccessible.InternetMaxBandwidthOut` defaults to `0Mbps`. When
  `PublicIpAssigned: true`, CVM disallows public IP assignment at `0`, so
  default `tencentInternetMaxBandwidthOutMbps` to `5` and reject any
  `PublicIpAssigned=true` payload with bandwidth `<= 0` before calling
  `RunInstances`.
- Render `InstanceName` through `tencentInstanceName(leaseID, slug)`: start
  with `crabbox-<leaseID>-<slug>`, sanitize to Tencent-safe display-name
  characters, and truncate the slug portion so the final string is at most
  128 characters. Preserve the `crabbox-<leaseID>-` prefix; do not let an
  overlong repo slug make launch fail.

## Fallback ordering

`createServerWithFallback` iterates in this order, mirroring AWS:

1. **Instance types** for the requested `class` —
   `tencentInstanceTypeCandidatesForTargetClass(config.class)` returns a
   ranked list (e.g. for `beast`: `["S5.LARGE16", "SA3.LARGE16",
   "S6.LARGE16"]`).
2. For each type, **zones** in the configured region. Pre-filter with
   `DescribeZoneInstanceConfigInfos` to avoid attempting types that aren't
   sold in a zone.
3. For each zone, **markets**: SPOTPAID first if `config.capacityMarket !==
   "on-demand"`, then `POSTPAID_BY_HOUR`.
4. Optionally fall back across **regions** —
   `tencentRegionCandidates(config, env, region)`. Build a fresh
   `TencentCVMClient` per region (analogous to `EC2SpotClient` per region).

Each attempt produces a `ProvisioningAttempt { region, serverType, market,
category, message }`. Break out of the loop early on non-retryable errors
(see `plans/08` for the classification rules).

## Image resolution

`resolveImage(config)`:

1. If `config.tencentImage` or `env.CRABBOX_TENCENT_IMAGE` is set, use it.
2. For Linux targets, call `DescribeImages` with
   `Filters=[{Name:"image-type",Values:["PUBLIC_IMAGE"]},
   {Name:"platform",Values:["Ubuntu"]}]`, sort by `CreatedTime` desc, take
   the latest `Ubuntu Server 22.04 LTS 64bit` ID. Do not use the image-family
   API here; it requires an explicit image-family name and does not resolve
   arbitrary public Ubuntu images.
3. Cache the result per region inside the client instance (process-lifetime
   only; Worker invocations are short-lived enough that this is fine).
4. Return the resolved `imageId` to the launch path and pass that exact ID to
   `RunInstances` and the pricing / SPOT cap helpers. Pricing must not invent
   a fake public image ID or run a second image resolver.
5. For Windows / macOS targets — Tencent CVM does not offer macOS. Throw a
   clear `tencent target=macos is not supported` error. Windows uses
   `Windows Server 2022 Datacenter Edition 64bit Chinese` or the English
   variant — make it env-overridable.

## Wait for IP

After `RunInstances`, poll `DescribeInstances` with the returned
`InstanceId` until `InstanceState === "RUNNING"` and `PublicIpAddresses[0]`
is non-empty. Mirror `EC2SpotClient.waitForServerIP`: total budget ~3 min,
1 s interval.

## Module placement

Lives in `worker/src/tencent.ts` (the main facade). Calls into
`tencent-signing.ts` for `tencentCall<T>`, `tencent-network.ts` for the SG
+ VPC ensure path, `tencent-pricing.ts` for SPOTPAID price caps, and
`tencent-types.ts` for request/response shapes.

## Returned `ProviderMachine`

```ts
{
  cloudID: instanceId,         // "ins-xxxxxxxx"
  serverID: instanceId,        // same; legacy field
  name: instanceName,
  status: instanceState.toLowerCase(),
  ip: publicIpAddresses[0],
  region: config.tencentRegion,
  serverType: instanceType,
  market: chargeTypeWasSpot ? "spot" : "on-demand",
  labels: tencentLabelsFromTags(tags),
  provider: "tencent",
}
```

## Open questions

- **macOS**: not supported by Tencent CVM. Confirm `tencent target=macos`
  fails fast in `config.ts` rather than reaching the launch path.
- **Windows bootstrap**: cloudbase-init is supported. The user-data we pass
  via `UserData` is executed by cloud-init on Linux and cloudbase-init on
  Windows. See `plans/05` for the bootstrap payload.
- **PublicIpAssigned vs. EIP**: default to assigned public IP for parity
  with AWS spot, with non-zero bandwidth as above. Document an opt-in for
  pre-existing EIPs as a stretch.

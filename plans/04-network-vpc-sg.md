# 04 — Network: VPC, subnet, security group

## Goal

Ensure a Crabbox-owned VPC + Subnet + Security Group exists in the target
region before `RunInstances` is called, or reuse the IDs provided via config.

Mirror: `AzureClient.ensureSharedInfra` and `EC2SpotClient.ensureSecurityGroup`.

Lives in `worker/src/tencent-network.ts`. Exposed to the launch path as a
single entry point `ensureSharedInfra(env, signing, region, zone, config)`
so the facade in `tencent.ts` can call it without knowing the per-step
details.

## Tencent API actions (vpc service)

| Step                          | Action                            |
| ----------------------------- | --------------------------------- |
| Look up Crabbox VPC by tag    | `DescribeVpcs`                    |
| Create VPC                    | `CreateVpc`                       |
| Look up subnet by tag         | `DescribeSubnets`                 |
| Create subnet (per zone)      | `CreateSubnet`                    |
| Look up SG by tag             | `DescribeSecurityGroups`          |
| Create SG                     | `CreateSecurityGroupWithPolicies` |
| Add ingress rule              | `CreateSecurityGroupPolicies`     |
| Remove stale ingress rule     | `DeleteSecurityGroupPolicies`     |

## Algorithm: `ensureSharedInfra(region, zone, config)`

1. **VPC.** If `config.tencentVPCID` or `env.CRABBOX_TENCENT_VPC_ID` is set,
   trust it. Otherwise:
   - `DescribeVpcs` filtered by tag `crabbox=true`. If one exists, take it.
   - Else `CreateVpc` with `VpcName=crabbox`, `CidrBlock=10.43.0.0/16`,
     `Tags=[{Key:"crabbox",Value:"true"}, {Key:"created_by",Value:"crabbox"}]`.
2. **Subnet.** If `config.tencentSubnetID` or
   `env.CRABBOX_TENCENT_SUBNET_ID` is set, trust it. Otherwise:
   - `DescribeSubnets` filtered by `vpc-id=<vpcID>` and `tag:crabbox=true`.
     Pick the one in `zone` if multiple exist.
   - Else `CreateSubnet` with `CidrBlock=10.43.<n>.0/24` where `n` derives
     from the zone index (zones are returned by `DescribeZones`; first =
     0, second = 1, …). Tag the subnet.
3. **Security group.** If `config.tencentSecurityGroupID` or
   `env.CRABBOX_TENCENT_SECURITY_GROUP_ID` is set, trust it. Otherwise:
   - `DescribeSecurityGroups` filtered by `tag:crabbox=true`. Take the one
     in the region.
   - Else `CreateSecurityGroupWithPolicies` with no initial rules.
4. **Ingress rules.** For each port in `sshPorts(config)` (22, plus webvnc
   ports if relevant) and each CIDR in `awsSSHCIDRs(config, env)`-equivalent
   (build `tencentSSHCIDRs`), ensure a `CreateSecurityGroupPolicies` entry
   exists for `(Action=ACCEPT, Protocol=TCP, Port=<port>, CidrBlock=<cidr>)`.
   Skip if already present.

## SSH CIDR resolution

Mirror `aws_ssh_cidr.go`-style logic but in TypeScript:

- If `config.tencentSSHCIDRs` is non-empty, use it.
- Else if the request has a known source IP (best-effort: `CF-Connecting-IP`
  header), tighten to that `/32`.
- Else fall back to `0.0.0.0/0` and log a `provider warning` attempt for the
  observability event stream.

This is the **same** logic AWS uses (`requestSourceCIDRs(request)` in
`fleet.ts`). Reuse it as-is; just hand it through to `tencentSSHCIDRs`.

## Tag schema

Crabbox-owned VPC/subnet/SG carry tags:

```
crabbox=true
created_by=crabbox
class=shared          # used for SG; subnets get class=<zone>
```

The CVM module reads only resources that carry `crabbox=true`. This keeps
us from accidentally touching user-managed infra.

## Open questions

- Tencent has both `default` and explicit VPCs. We always use an explicit
  Crabbox-owned VPC for clarity, same as Azure. If a user prefers `CreateDefaultVpc`,
  let them pass `tencentVPCID="default"`; handle in a follow-up.
- IPv6: out of scope.
- Multiple regions: each region needs its own VPC + SG. The ensure path is
  region-scoped, so a `TencentCVMClient` per region cleanly avoids
  cross-region state.

## Acceptance for this plan

- `ensureSharedInfra` is idempotent: calling it twice within a few seconds
  does not create duplicate VPC/subnet/SG resources.
- A fresh Tencent account with no VPC ends up with exactly one Crabbox-tagged
  VPC, one subnet per zone we've launched into, one SG, and one ingress
  rule per `(port, cidr)` pair.

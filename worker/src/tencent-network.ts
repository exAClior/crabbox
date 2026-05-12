import { sshPorts, validCIDRs, type LeaseConfig } from "./config";
import type { TencentCaller, TencentSharedInfra, TencentTag } from "./tencent-types";
import type { Env } from "./types";

interface VPCResponse {
  VpcSet?: Array<{ VpcId?: string; VpcName?: string; TagSet?: TencentTag[] }>;
}

interface CreateVPCResponse {
  Vpc?: { VpcId?: string };
}

interface SubnetResponse {
  SubnetSet?: Array<{ SubnetId?: string; VpcId?: string; Zone?: string; TagSet?: TencentTag[] }>;
}

interface CreateSubnetResponse {
  Subnet?: { SubnetId?: string };
}

interface SecurityGroupResponse {
  SecurityGroupSet?: Array<{ SecurityGroupId?: string; SecurityGroupName?: string }>;
}

interface CreateSecurityGroupResponse {
  SecurityGroup?: { SecurityGroupId?: string };
}

interface SecurityGroupPoliciesResponse {
  SecurityGroupPolicySet?: {
    Ingress?: SecurityGroupPolicy[];
  };
}

interface SecurityGroupPolicy {
  Protocol?: string;
  Port?: string;
  CidrBlock?: string;
  Ipv6CidrBlock?: string;
  Action?: string;
  PolicyDescription?: string;
}

const crabboxTags: TencentTag[] = [
  { Key: "crabbox", Value: "true" },
  { Key: "created_by", Value: "crabbox" },
];

export async function ensureTencentSharedInfra(
  call: TencentCaller,
  env: Env,
  region: string,
  zone: string,
  config: LeaseConfig,
): Promise<TencentSharedInfra> {
  void region;
  const vpcID = await ensureVPC(call, config.tencentVPCID || env.CRABBOX_TENCENT_VPC_ID || "");
  const subnetID = await ensureSubnet(
    call,
    vpcID,
    zone,
    config.tencentSubnetID || env.CRABBOX_TENCENT_SUBNET_ID || "",
  );
  const securityGroupID = await ensureSecurityGroup(
    call,
    config.tencentSecurityGroupID || env.CRABBOX_TENCENT_SECURITY_GROUP_ID || "",
  );
  await ensureIngressPolicies(
    call,
    securityGroupID,
    tencentSSHCIDRs(config, env),
    sshPorts(config),
  );
  return { vpcID, subnetID, securityGroupID };
}

function tencentSSHCIDRs(config: LeaseConfig, env: Env): string[] {
  const cidrs = validCIDRs([
    ...config.tencentSSHCIDRs,
    ...(env.CRABBOX_TENCENT_SSH_CIDRS ?? "").split(","),
  ]);
  return cidrs.length > 0 ? cidrs : ["0.0.0.0/0"];
}

async function ensureVPC(call: TencentCaller, configuredID: string): Promise<string> {
  if (configuredID.trim()) {
    return configuredID.trim();
  }
  const existing = await call<VPCResponse>("vpc", "DescribeVpcs", {
    Filters: [{ Name: "tag:crabbox", Values: ["true"] }],
    Offset: "0",
    Limit: "20",
  });
  const existingID = firstID(existing.VpcSet, "VpcId");
  if (existingID) {
    return existingID;
  }
  const created = await call<CreateVPCResponse>("vpc", "CreateVpc", {
    VpcName: "crabbox",
    CidrBlock: "10.43.0.0/16",
    Tags: [...crabboxTags, { Key: "class", Value: "shared" }],
  });
  const vpcID = created.Vpc?.VpcId ?? "";
  if (!vpcID) {
    throw new Error("tencent CreateVpc returned no VpcId");
  }
  return vpcID;
}

async function ensureSubnet(
  call: TencentCaller,
  vpcID: string,
  zone: string,
  configuredID: string,
): Promise<string> {
  if (configuredID.trim()) {
    return configuredID.trim();
  }
  const existing = await call<SubnetResponse>("vpc", "DescribeSubnets", {
    Filters: [
      { Name: "vpc-id", Values: [vpcID] },
      { Name: "tag:crabbox", Values: ["true"] },
    ],
    Offset: "0",
    Limit: "100",
  });
  const zoneSubnet = (existing.SubnetSet ?? []).find((subnet) => subnet.Zone === zone)?.SubnetId;
  if (zoneSubnet) {
    return zoneSubnet;
  }
  const created = await call<CreateSubnetResponse>("vpc", "CreateSubnet", {
    VpcId: vpcID,
    SubnetName: `crabbox-${zone}`.slice(0, 60),
    CidrBlock: subnetCIDRForZone(zone),
    Zone: zone,
    Tags: [...crabboxTags, { Key: "class", Value: zone }],
  });
  const subnetID = created.Subnet?.SubnetId ?? "";
  if (!subnetID) {
    throw new Error("tencent CreateSubnet returned no SubnetId");
  }
  return subnetID;
}

async function ensureSecurityGroup(call: TencentCaller, configuredID: string): Promise<string> {
  if (configuredID.trim()) {
    return configuredID.trim();
  }
  const existing = await call<SecurityGroupResponse>("vpc", "DescribeSecurityGroups", {
    Filters: [{ Name: "tag:crabbox", Values: ["true"] }],
    Offset: "0",
    Limit: "20",
  });
  const existingID = firstID(existing.SecurityGroupSet, "SecurityGroupId");
  if (existingID) {
    return existingID;
  }
  const created = await call<CreateSecurityGroupResponse>(
    "vpc",
    "CreateSecurityGroupWithPolicies",
    {
      GroupName: "crabbox-runners",
      GroupDescription: "Crabbox ephemeral test runners",
      ProjectId: "0",
      SecurityGroupPolicySet: { Ingress: [], Egress: [] },
      Tags: [...crabboxTags, { Key: "class", Value: "shared" }],
    },
  );
  const securityGroupID = created.SecurityGroup?.SecurityGroupId ?? "";
  if (!securityGroupID) {
    throw new Error("tencent CreateSecurityGroupWithPolicies returned no SecurityGroupId");
  }
  return securityGroupID;
}

async function ensureIngressPolicies(
  call: TencentCaller,
  securityGroupID: string,
  cidrs: string[],
  ports: string[],
): Promise<void> {
  const policies = await call<SecurityGroupPoliciesResponse>(
    "vpc",
    "DescribeSecurityGroupPolicies",
    {
      SecurityGroupId: securityGroupID,
      Filters: [{ Name: "direction", Values: ["INBOUND"] }],
    },
  );
  const existing = policies.SecurityGroupPolicySet?.Ingress ?? [];
  const missing: SecurityGroupPolicy[] = [];
  for (const port of ports) {
    for (const cidr of cidrs) {
      if (!hasPolicy(existing, port, cidr)) {
        const policy: SecurityGroupPolicy = {
          Action: "ACCEPT",
          Protocol: "TCP",
          Port: port,
          PolicyDescription: "Crabbox SSH",
        };
        if (cidr.includes(":")) {
          policy.Ipv6CidrBlock = cidr;
        } else {
          policy.CidrBlock = cidr;
        }
        missing.push(policy);
      }
    }
  }
  if (missing.length === 0) {
    return;
  }
  await call("vpc", "CreateSecurityGroupPolicies", {
    SecurityGroupId: securityGroupID,
    SecurityGroupPolicySet: { Ingress: missing },
  });
}

function hasPolicy(policies: SecurityGroupPolicy[], port: string, cidr: string): boolean {
  return policies.some(
    (policy) =>
      (policy.Action ?? "").toUpperCase() === "ACCEPT" &&
      (policy.Protocol ?? "").toUpperCase() === "TCP" &&
      String(policy.Port ?? "") === port &&
      ((cidr.includes(":") && policy.Ipv6CidrBlock === cidr) ||
        (!cidr.includes(":") && policy.CidrBlock === cidr)),
  );
}

function subnetCIDRForZone(zone: string): string {
  const zoneIndex = Math.max(0, Math.min(250, Number(zone.match(/-(\d+)$/)?.[1] ?? "1") - 1));
  return `10.43.${zoneIndex}.0/24`;
}

function firstID(items: unknown[] | undefined, key: string): string {
  for (const item of items ?? []) {
    const value = record(item)[key];
    if (typeof value === "string" && value) {
      return value;
    }
  }
  return "";
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

import { awsUserData, windowsBootstrapPowerShell } from "./bootstrap";
import {
  tencentInstanceTypeCandidatesForTargetClass,
  tencentInternetMaxBandwidthOutMbps,
  tencentSystemDiskGB,
  tencentSystemDiskTypeFor,
  type LeaseConfig,
} from "./config";
import { leaseProviderLabels } from "./provider-labels";
import { ensureTencentSharedInfra } from "./tencent-network";
import {
  tencentHourlyPriceUSD,
  tencentQuotaPreflightAttempt,
  tencentSpotMaxPrice,
  tencentStaticHourlyPriceUSD,
} from "./tencent-pricing";
import { tencentCall, TencentAPIError } from "./tencent-signing";
import type {
  TencentCaller,
  TencentChargeType,
  TencentCreateImageResponse,
  TencentDescribeImagesResponse,
  TencentDescribeInstancesResponse,
  TencentDescribeKeyPairsResponse,
  TencentDescribeZoneInstanceConfigInfosResponse,
  TencentDescribeZonesResponse,
  TencentImage,
  TencentImportKeyPairResponse,
  TencentInstance,
  TencentMarket,
  TencentRunInstancesPayload,
  TencentRunInstancesResponse,
  TencentService,
  TencentSharedInfra,
  TencentTag,
} from "./tencent-types";
import type { Env, ProviderImage, ProviderMachine, ProvisioningAttempt } from "./types";

const cvmVersion = "2017-03-12";
const vpcVersion = "2017-03-12";
const maxDescribeInstanceIDs = 100;
const maxInstanceNameLength = 128;

export class TencentCVMClient {
  private readonly imageCache = new Map<string, string>();
  private readonly region: string;
  private readonly call: TencentCaller;

  constructor(
    private readonly env: Env,
    region?: string,
  ) {
    this.region = region?.trim() || env.CRABBOX_TENCENT_REGION?.trim() || "ap-singapore";
    this.call = async <T>(
      service: TencentService,
      action: string,
      payload: Record<string, unknown>,
    ) =>
      tencentCall<T>(env, {
        service,
        version: service === "vpc" ? vpcVersion : cvmVersion,
        action,
        region: this.region,
        payload,
      });
  }

  async listCrabboxServers(): Promise<ProviderMachine[]> {
    const machines: ProviderMachine[] = [];
    let offset = 0;
    let total = Number.POSITIVE_INFINITY;
    while (offset < total) {
      // oxlint-disable-next-line eslint/no-await-in-loop -- Tencent pagination depends on the previous page size.
      const response = await this.call<TencentDescribeInstancesResponse>(
        "cvm",
        "DescribeInstances",
        {
          Filters: [{ Name: "tag:crabbox", Values: ["true"] }],
          Offset: offset,
          Limit: 100,
        },
      );
      total = response.TotalCount ?? 0;
      const instances = response.InstanceSet ?? [];
      machines.push(...instances.map((instance) => this.instanceToMachine(instance)));
      if (instances.length === 0) {
        break;
      }
      offset += instances.length;
    }
    return machines;
  }

  async createServerWithFallback(
    config: LeaseConfig,
    leaseID: string,
    slug: string,
    owner: string,
  ): Promise<{
    server: ProviderMachine;
    serverType: string;
    market?: string;
    attempts?: ProvisioningAttempt[];
  }> {
    if (config.target === "macos") {
      throw new Error("tencent target=macos is not supported");
    }
    const imageID = await this.resolveImage(config);
    config.tencentImage = imageID;
    const keyID =
      config.target === "linux"
        ? await this.ensureKeyPair(config.providerKey, config.sshPublicKey)
        : "";
    const candidates = tencentLaunchCandidates(config);
    const zones = await this.zonesForRegion(config);
    const attempts: ProvisioningAttempt[] = [];
    const failures: string[] = [];
    const infraByZone = new Map<string, TencentSharedInfra>();

    for (const serverType of candidates) {
      // oxlint-disable-next-line eslint/no-await-in-loop -- instance-type fallback must preserve ordered capacity preference.
      const typeZones = await this.availableZonesForInstanceType(serverType, zones);
      for (const zone of typeZones) {
        for (const chargeType of chargeTypesForConfig(config)) {
          const market = tencentMarketForChargeType(chargeType);
          // oxlint-disable-next-line eslint/no-await-in-loop -- quota preflight follows sequential fallback order.
          const preflight = await tencentQuotaPreflightAttempt({
            call: this.call,
            region: this.region,
            zone,
            serverType,
            market,
          });
          if (preflight) {
            attempts.push(preflight);
            failures.push(`${zone}/${serverType}/${market}: ${preflight.message}`);
            continue;
          }
          let infra = infraByZone.get(zone);
          if (!infra) {
            // oxlint-disable-next-line eslint/no-await-in-loop -- infra is zone-scoped and created only for the selected fallback attempt.
            infra = await ensureTencentSharedInfra(this.call, this.env, this.region, zone, config);
            infraByZone.set(zone, infra);
          }
          try {
            // oxlint-disable-next-line eslint/no-await-in-loop -- launch attempts must be sequential to avoid duplicate capacity burns.
            const server = await this.createServer(
              { ...config, serverType, tencentZone: zone },
              leaseID,
              slug,
              owner,
              imageID,
              keyID,
              infra,
              chargeType,
            );
            // oxlint-disable-next-line eslint/no-await-in-loop -- wait only after the attempt that actually created an instance.
            const ready = await this.waitForServerIP(server.cloudID);
            const result: {
              server: ProviderMachine;
              serverType: string;
              market?: string;
              attempts?: ProvisioningAttempt[];
            } = { server: { ...ready, region: this.region }, serverType, market };
            if (attempts.length > 0) {
              result.attempts = attempts;
            }
            return result;
          } catch (error) {
            const message = error instanceof Error ? error.message : String(error);
            attempts.push({
              region: zone,
              serverType,
              market,
              category: tencentProvisioningErrorCategory(message) || "fatal",
              message: conciseTencentProvisioningMessage(message),
            });
            failures.push(`${zone}/${serverType}/${market}: ${message}`);
            if (!isRetryableTencentProvisioningError(message)) {
              throw new Error(failures.join("; "), { cause: error });
            }
          }
        }
      }
    }
    if (config.serverTypeExplicit) {
      throw new Error(
        `requested exact Tencent instance type ${config.serverType} failed; remove --type to allow class fallback: ${failures.join("; ")}`,
      );
    }
    throw new Error(failures.join("; "));
  }

  async waitForServerIP(instanceID: string): Promise<ProviderMachine> {
    const deadline = Date.now() + 180_000;
    while (Date.now() < deadline) {
      // oxlint-disable-next-line eslint/no-await-in-loop -- polling must observe Tencent state transitions in order.
      const server = await this.getServer(instanceID);
      if (server.status === "running" && server.host) {
        return server;
      }
      // oxlint-disable-next-line eslint/no-await-in-loop -- this delay is the polling interval.
      await sleep(1_000);
    }
    throw new Error(`timed out waiting for Tencent instance public IP: ${instanceID}`);
  }

  async getServer(instanceID: string): Promise<ProviderMachine> {
    const instances = await this.describeInstancesByIDs([instanceID]);
    const instance = instances[0];
    if (!instance) {
      throw new Error(`tencent instance not found: ${instanceID}`);
    }
    return this.instanceToMachine(instance);
  }

  async deleteServer(instanceID: string): Promise<void> {
    await this.call("cvm", "TerminateInstances", {
      InstanceIds: [instanceID],
      ReleaseAddress: true,
    }).catch((error: unknown) => {
      if (!isTencentNotFound(error)) {
        throw error;
      }
    });
  }

  async stopInstance(instanceID: string, mode: "soft" | "dealloc" = "dealloc"): Promise<void> {
    await this.call("cvm", "StopInstances", {
      InstanceIds: [instanceID],
      StopType: "SOFT_FIRST",
      StoppedMode: mode === "soft" ? "KEEP_CHARGING" : "STOP_CHARGING",
    });
  }

  async startInstance(instanceID: string): Promise<void> {
    await this.call("cvm", "StartInstances", { InstanceIds: [instanceID] });
  }

  async rebootInstance(instanceID: string, force = false): Promise<void> {
    await this.call("cvm", "RebootInstances", {
      InstanceIds: [instanceID],
      StopType: force ? "HARD" : "SOFT_FIRST",
    });
  }

  async createImage(instanceID: string, name: string, noReboot: boolean): Promise<ProviderImage> {
    const imageName = name.slice(0, 60);
    const response = await this.call<TencentCreateImageResponse>("cvm", "CreateImage", {
      InstanceId: instanceID,
      ImageName: imageName,
      ImageDescription: `Crabbox cached image for ${imageName}`.slice(0, 256),
      ForcePoweroff: noReboot ? "FALSE" : "TRUE",
      TagSpecification: [
        {
          ResourceType: "image",
          Tags: [
            { Key: "crabbox", Value: "true" },
            { Key: "created_by", Value: "crabbox" },
            { Key: "Name", Value: imageName },
          ],
        },
      ],
    });
    const imageID = response.ImageId ?? "";
    if (!imageID) {
      throw new Error("tencent CreateImage returned no ImageId");
    }
    return { id: imageID, name: imageName, state: "pending", region: this.region };
  }

  async getImage(imageID: string): Promise<ProviderImage> {
    const response = await this.call<TencentDescribeImagesResponse>("cvm", "DescribeImages", {
      ImageIds: [imageID],
    });
    const image = response.ImageSet?.[0];
    if (!image?.ImageId) {
      throw new Error(`tencent image not found: ${imageID}`);
    }
    return this.imageToProviderImage(image);
  }

  async listCrabboxImages(): Promise<ProviderImage[]> {
    const response = await this.call<TencentDescribeImagesResponse>("cvm", "DescribeImages", {
      Filters: [{ Name: "tag:crabbox", Values: ["true"] }],
    });
    return (response.ImageSet ?? []).map((image) => this.imageToProviderImage(image));
  }

  async deleteImage(imageID: string): Promise<void> {
    await this.call("cvm", "DeleteImages", { ImageIds: [imageID], DeleteBindedSnap: false }).catch(
      (error: unknown) => {
        if (!isTencentNotFound(error)) {
          throw error;
        }
      },
    );
  }

  async deleteSSHKey(name: string): Promise<void> {
    const keyID = await this.keyPairIDForName(name);
    if (!keyID) {
      return;
    }
    await this.call("cvm", "DeleteKeyPairs", { KeyIds: [keyID] }).catch((error: unknown) => {
      if (!isTencentNotFound(error)) {
        throw error;
      }
    });
  }

  async hourlyPriceUSD(serverType: string, config: LeaseConfig): Promise<number | undefined> {
    const imageID = config.tencentImage || this.env.CRABBOX_TENCENT_IMAGE || "";
    if (!/^img-[A-Za-z0-9]+$/.test(imageID)) {
      return tencentStaticHourlyPriceUSD(serverType, this.env);
    }
    const zone = config.tencentZone || this.env.CRABBOX_TENCENT_ZONE || `${this.region}-1`;
    return tencentHourlyPriceUSD({
      call: this.call,
      env: this.env,
      region: this.region,
      zone,
      serverType,
      config,
      chargeType: config.capacityMarket === "on-demand" ? "POSTPAID_BY_HOUR" : "SPOTPAID",
      imageId: imageID,
    });
  }

  private async createServer(
    config: LeaseConfig,
    leaseID: string,
    slug: string,
    owner: string,
    imageID: string,
    keyID: string,
    infra: TencentSharedInfra,
    chargeType: TencentChargeType,
  ): Promise<ProviderMachine> {
    const name = tencentInstanceName(leaseID, slug);
    const market = tencentMarketForChargeType(chargeType);
    const labels = leaseProviderLabels(config, leaseID, slug, owner, "tencent", new Date(), {
      market,
    });
    const bandwidth = tencentInternetMaxBandwidthOutMbps(config, this.env);
    const publicIPAssigned = true;
    if (publicIPAssigned && bandwidth <= 0) {
      throw new Error(
        "tencent PublicIpAssigned=true requires tencentInternetMaxBandwidthOutMbps > 0",
      );
    }
    const payload: TencentRunInstancesPayload = {
      InstanceChargeType: chargeType,
      Placement: { Zone: config.tencentZone, ProjectId: 0 },
      InstanceType: config.serverType,
      ImageId: imageID,
      SystemDisk: {
        DiskType: tencentSystemDiskTypeFor(
          config.tencentSystemDiskType,
          this.env.CRABBOX_TENCENT_SYSTEM_DISK_TYPE,
        ),
        DiskSize: tencentSystemDiskGB(config, this.env),
      },
      VirtualPrivateCloud: { VpcId: infra.vpcID, SubnetId: infra.subnetID },
      InternetAccessible: {
        InternetChargeType: "TRAFFIC_POSTPAID_BY_HOUR",
        InternetMaxBandwidthOut: bandwidth,
        PublicIpAssigned: publicIPAssigned,
      },
      InstanceCount: 1,
      InstanceName: name,
      SecurityGroupIds: [infra.securityGroupID],
      UserData: btoa(tencentUserData(config)),
      ClientToken: leaseID,
      TagSpecification: [
        { ResourceType: "instance", Tags: tencentTagsFromLabels({ ...labels, Name: name }) },
      ],
    };
    if (keyID) {
      payload.LoginSettings = { KeyIds: [keyID] };
    }
    if (chargeType === "SPOTPAID") {
      const maxPrice = await tencentSpotMaxPrice({
        call: this.call,
        env: this.env,
        region: this.region,
        zone: config.tencentZone,
        serverType: config.serverType,
        config,
        imageId: imageID,
        vpcID: infra.vpcID,
        subnetID: infra.subnetID,
      });
      payload.InstanceMarketOptions = {
        MarketType: "spot",
        SpotOptions: {
          SpotInstanceType: "one-time",
          ...(maxPrice ? { MaxPrice: maxPrice } : {}),
        },
      };
    }
    const response = await this.call<TencentRunInstancesResponse>("cvm", "RunInstances", {
      ...payload,
    });
    const instanceID = response.InstanceIdSet?.[0] ?? "";
    if (!instanceID) {
      throw new Error("tencent RunInstances returned no InstanceIdSet");
    }
    return {
      provider: "tencent",
      id: 0,
      cloudID: instanceID,
      name,
      status: "pending",
      serverType: config.serverType,
      host: "",
      region: this.region,
      labels: { ...labels, Name: name },
    };
  }

  private async resolveImage(config: LeaseConfig): Promise<string> {
    const explicit = config.tencentImage || this.env.CRABBOX_TENCENT_IMAGE || "";
    if (explicit.trim()) {
      return explicit.trim();
    }
    if (config.target === "macos") {
      throw new Error("tencent target=macos is not supported");
    }
    const cacheKey = `${this.region}:${config.target}`;
    const cached = this.imageCache.get(cacheKey);
    if (cached) {
      return cached;
    }
    const response = await this.call<TencentDescribeImagesResponse>("cvm", "DescribeImages", {
      Filters: [
        { Name: "image-type", Values: ["PUBLIC_IMAGE"] },
        { Name: "platform", Values: [config.target === "windows" ? "Windows" : "Ubuntu"] },
      ],
    });
    const sorted = (response.ImageSet ?? []).toSorted((left, right) =>
      (right.CreatedTime ?? "").localeCompare(left.CreatedTime ?? ""),
    );
    const selected =
      config.target === "windows" ? latestWindowsImage(sorted) : latestUbuntuImage(sorted);
    const imageID = selected?.ImageId ?? "";
    if (!imageID) {
      throw new Error(`no Tencent ${config.target} public image found in ${this.region}`);
    }
    this.imageCache.set(cacheKey, imageID);
    return imageID;
  }

  private async ensureKeyPair(name: string, publicKey: string): Promise<string> {
    if (name.length > 25) {
      throw new Error("tencent key pair name must be at most 25 characters");
    }
    const existing = await this.keyPairIDForName(name);
    if (existing) {
      return existing;
    }
    const response = await this.call<TencentImportKeyPairResponse>("cvm", "ImportKeyPair", {
      KeyName: name,
      PublicKey: publicKey,
      ProjectId: 0,
      TagSpecification: [
        {
          ResourceType: "keypair",
          Tags: [
            { Key: "crabbox", Value: "true" },
            { Key: "created_by", Value: "crabbox" },
            { Key: "owner", Value: "crabbox" },
          ],
        },
      ],
    });
    const keyID = response.KeyId ?? "";
    if (!keyID) {
      throw new Error("tencent ImportKeyPair returned no KeyId");
    }
    return keyID;
  }

  private async keyPairIDForName(name: string): Promise<string> {
    const response = await this.call<TencentDescribeKeyPairsResponse>("cvm", "DescribeKeyPairs", {
      Filters: [{ Name: "key-name", Values: [name] }],
      Offset: 0,
      Limit: 20,
    });
    return response.KeyPairSet?.find((key) => key.KeyName === name)?.KeyId ?? "";
  }

  private async describeInstancesByIDs(instanceIDs: string[]): Promise<TencentInstance[]> {
    const instances: TencentInstance[] = [];
    for (const chunk of chunks(instanceIDs, maxDescribeInstanceIDs)) {
      // oxlint-disable-next-line eslint/no-await-in-loop -- Tencent caps DescribeInstances.InstanceIds at 100.
      const response = await this.call<TencentDescribeInstancesResponse>(
        "cvm",
        "DescribeInstances",
        { InstanceIds: chunk },
      );
      instances.push(...(response.InstanceSet ?? []));
    }
    return instances;
  }

  private async zonesForRegion(config: LeaseConfig): Promise<string[]> {
    const configured = config.tencentZone || this.env.CRABBOX_TENCENT_ZONE || "";
    if (configured.trim()) {
      return [configured.trim()];
    }
    const response = await this.call<TencentDescribeZonesResponse>("cvm", "DescribeZones", {});
    const zones = (response.ZoneSet ?? [])
      .map((zone) => zone.Zone ?? "")
      .filter((zone) => zone.startsWith(this.region));
    return uniqueStrings(zones.length > 0 ? zones : [`${this.region}-1`]);
  }

  private async availableZonesForInstanceType(
    serverType: string,
    zones: string[],
  ): Promise<string[]> {
    try {
      const response = await this.call<TencentDescribeZoneInstanceConfigInfosResponse>(
        "cvm",
        "DescribeZoneInstanceConfigInfos",
        { Filters: [{ Name: "instance-type", Values: [serverType] }] },
      );
      const available = (response.InstanceTypeQuotaSet ?? [])
        .filter(
          (item) =>
            zones.includes(item.Zone ?? "") &&
            (item.Status ?? "SELL") === "SELL" &&
            (item.StatusCategory ?? "NormalStock") !== "WithoutStock",
        )
        .map((item) => item.Zone ?? "");
      return uniqueStrings(available.length > 0 ? available : zones);
    } catch {
      return zones;
    }
  }

  private imageToProviderImage(image: TencentImage): ProviderImage {
    return {
      id: image.ImageId ?? "",
      name: image.ImageName ?? image.ImageId ?? "",
      state: tencentImageState(image.ImageState ?? ""),
      region: this.region,
    };
  }

  private instanceToMachine(instance: TencentInstance): ProviderMachine {
    const cloudID = instance.InstanceId ?? "";
    const labels = tencentLabelsFromTags(instance.Tags ?? []);
    const state = (instance.InstanceState ?? "").toLowerCase();
    return {
      provider: "tencent",
      id: 0,
      cloudID,
      region: this.region,
      name: instance.InstanceName || labels["Name"] || cloudID,
      status: state,
      serverType: instance.InstanceType ?? "",
      host: instance.PublicIpAddresses?.[0] ?? "",
      labels,
    };
  }
}

export function tencentLaunchCandidates(config: LeaseConfig): string[] {
  if (config.serverTypeExplicit) {
    return [config.serverType];
  }
  return uniqueStrings([
    config.serverType,
    ...tencentInstanceTypeCandidatesForTargetClass(config.target, config.class, config.windowsMode),
    "S5.MEDIUM4",
  ]);
}

export async function tencentZoneForRegion(
  call: TencentCaller,
  config: Pick<LeaseConfig, "tencentZone">,
  env: Pick<Env, "CRABBOX_TENCENT_ZONE">,
  region: string,
): Promise<string> {
  const configured = config.tencentZone || env.CRABBOX_TENCENT_ZONE || "";
  if (configured.trim()) {
    return configured.trim();
  }
  const response = await call<TencentDescribeZonesResponse>("cvm", "DescribeZones", {});
  return (
    response.ZoneSet?.map((zone) => zone.Zone ?? "").find((zone) => zone.startsWith(region)) ||
    `${region}-1`
  );
}

export function tencentTagsFromLabels(labels: Record<string, string>): TencentTag[] {
  const tags: TencentTag[] = [];
  for (const [key, value] of Object.entries(labels)) {
    if (isReservedTencentTagKey(key)) {
      continue;
    }
    const tagKey = sanitizeTencentTagKey(key);
    if (!tagKey) {
      continue;
    }
    tags.push({ Key: tagKey, Value: sanitizeTencentTagValue(value) });
  }
  return tags;
}

export function tencentLabelsFromTags(tags: TencentTag[]): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const tag of tags) {
    if (tag.Key) {
      labels[tag.Key] = tag.Value ?? "";
    }
  }
  return labels;
}

export function tencentInstanceName(leaseID: string, slug: string): string {
  const prefix = `crabbox-${leaseID}-`;
  const safeSlug =
    slug
      .trim()
      .replaceAll(/[^a-zA-Z0-9_.-]/g, "-")
      .replaceAll(/-+/g, "-")
      .replaceAll(/^-+|-+$/g, "") || "lease";
  const room = Math.max(0, maxInstanceNameLength - prefix.length);
  return `${prefix}${safeSlug.slice(0, room)}`;
}

export function validTencentProviderKey(value: string | undefined): value is string {
  return typeof value === "string" && /^crabbox-cbx-[a-f0-9]{12}$/.test(value);
}

export function tencentProvisioningErrorCategory(message: string): string {
  if (message.includes("InternalError") || message.includes("InternalServerError")) {
    return "transient";
  }
  if (message.includes("RequestLimitExceeded")) {
    return "throttling";
  }
  if (
    message.includes("ResourceInsufficient.SpecifiedInstanceType") ||
    message.includes("ResourceInsufficient.AvailabilityZoneSoldOut") ||
    message.includes("ResourceInsufficient") ||
    message.includes("SoldOut")
  ) {
    return "capacity";
  }
  if (message.includes("LimitExceeded.UserAccountQuota")) {
    return "quota";
  }
  if (message.includes("AuthFailure") || message.includes("UnauthorizedOperation")) {
    return "auth";
  }
  if (message.includes("OperationDenied")) {
    return "policy";
  }
  if (message.includes("InvalidParameterValue")) {
    return "invalid";
  }
  return "";
}

export function isRetryableTencentProvisioningError(message: string): boolean {
  const category = tencentProvisioningErrorCategory(message);
  return category === "transient" || category === "throttling" || category === "capacity";
}

function chargeTypesForConfig(config: LeaseConfig): TencentChargeType[] {
  if (config.capacityMarket === "on-demand") {
    return ["POSTPAID_BY_HOUR"];
  }
  if (config.tencentInstanceChargeType === "SPOTPAID") {
    return ["SPOTPAID", "POSTPAID_BY_HOUR"];
  }
  return ["SPOTPAID", "POSTPAID_BY_HOUR"];
}

function tencentMarketForChargeType(chargeType: TencentChargeType): TencentMarket {
  return chargeType === "SPOTPAID" ? "spot" : "on-demand";
}

function tencentUserData(config: LeaseConfig): string {
  if (config.target === "windows") {
    return `<powershell>\n${windowsBootstrapPowerShell(config)}\n</powershell>`;
  }
  return awsUserData(config);
}

function latestUbuntuImage(images: TencentImage[]): TencentImage | undefined {
  return (
    images.find((image) => /ubuntu server 22\.04.*64/i.test(image.ImageName ?? "")) ??
    images.find((image) =>
      /ubuntu.*22\.04/i.test(`${image.ImageName ?? ""} ${image.OsName ?? ""}`),
    ) ??
    images.find((image) => /ubuntu/i.test(`${image.ImageName ?? ""} ${image.OsName ?? ""}`))
  );
}

function latestWindowsImage(images: TencentImage[]): TencentImage | undefined {
  return (
    images.find((image) => /windows server 2022/i.test(image.ImageName ?? "")) ??
    images.find((image) => /windows server/i.test(`${image.ImageName ?? ""} ${image.OsName ?? ""}`))
  );
}

function tencentImageState(state: string): string {
  switch (state.toUpperCase()) {
    case "NORMAL":
      return "available";
    case "CREATING":
    case "SYNCING":
    case "IMPORTING":
      return "pending";
    case "USING":
      return "in-use";
    case "CREATEFAILED":
    case "IMPORTFAILED":
      return "failed";
    default:
      return state.toLowerCase() || "unknown";
  }
}

function sanitizeTencentTagKey(key: string): string {
  return key
    .trim()
    .replaceAll(/[^a-zA-Z0-9_.-]/g, "_")
    .slice(0, 127)
    .replaceAll(/^[_.-]+|[_.-]+$/g, "");
}

function sanitizeTencentTagValue(value: string): string {
  return value
    .trim()
    .replaceAll(/[^a-zA-Z0-9_.-]/g, "_")
    .slice(0, 255)
    .replaceAll(/^[_.-]+|[_.-]+$/g, "");
}

function isReservedTencentTagKey(key: string): boolean {
  const normalized = key.toLowerCase();
  return (
    normalized.startsWith("tencent:") ||
    normalized.startsWith("qcloud:") ||
    normalized.startsWith("aws:")
  );
}

function isTencentNotFound(error: unknown): boolean {
  if (error instanceof TencentAPIError) {
    return /NotFound|Invalid.*NotFound/i.test(error.code);
  }
  const message = error instanceof Error ? error.message : String(error);
  return /NotFound|Invalid.*NotFound/i.test(message);
}

function conciseTencentProvisioningMessage(message: string): string {
  const match = /tencent [^:]+: ([A-Za-z0-9_.-]+):\s*(.*)$/.exec(message);
  if (match?.[1]) {
    return `${match[1]}: ${(match[2] ?? "").slice(0, 300)}`;
  }
  return message.replace(/\s+/g, " ").slice(0, 500);
}

function uniqueStrings(values: string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    const normalized = value.trim();
    if (normalized && !seen.has(normalized)) {
      seen.add(normalized);
      out.push(normalized);
    }
  }
  return out;
}

function chunks<T>(items: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let index = 0; index < items.length; index += size) {
    out.push(items.slice(index, index + size));
  }
  return out;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

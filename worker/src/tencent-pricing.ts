import {
  tencentInternetMaxBandwidthOutMbps,
  tencentSystemDiskGB,
  tencentSystemDiskTypeFor,
} from "./config";
import type { TencentPricingOptions, TencentQuotaPreflightOptions } from "./tencent-types";
import type { Env, ProvisioningAttempt } from "./types";

interface PriceResponse {
  Price?: {
    InstancePrice?: ItemPrice;
    BandwidthPrice?: ItemPrice;
  };
}

interface ItemPrice {
  UnitPrice?: number;
  UnitPriceDiscount?: number;
  ChargeUnit?: string;
}

interface AccountQuotaResponse {
  AccountQuotaOverview?: {
    Region?: string;
    AccountQuota?: {
      PostPaidQuotaSet?: QuotaItem[];
      SpotPaidQuotaSet?: QuotaItem[];
    };
  };
}

interface QuotaItem {
  UsedQuota?: number;
  RemainingQuota?: number;
  TotalQuota?: number;
  Zone?: string;
}

const staticHourlyCNY: Record<string, number> = {
  "S5.SMALL2": 0.1,
  "S5.MEDIUM4": 0.2,
  "S5.LARGE8": 0.4,
  "S5.LARGE16": 0.8,
  "S5.2XLARGE16": 0.9,
  "SA3.MEDIUM4": 0.18,
  "SA3.LARGE8": 0.36,
  "SA3.LARGE16": 0.8,
  "SA3.2XLARGE16": 0.82,
  "S6.MEDIUM4": 0.22,
  "S6.LARGE8": 0.44,
  "S6.LARGE16": 0.88,
  "S6.2XLARGE16": 0.92,
};

export async function tencentHourlyPriceUSD(
  options: TencentPricingOptions,
): Promise<number | undefined> {
  if (!options.imageId.trim()) {
    throw new Error("tencent pricing requires a resolved imageId");
  }
  try {
    const response = await options.call<PriceResponse>("cvm", "InquiryPriceRunInstances", {
      ...tencentPricingPayload(options),
    });
    const instance = hourlyCNY(response.Price?.InstancePrice);
    const bandwidth = hourlyCNY(response.Price?.BandwidthPrice) ?? 0;
    if (instance !== undefined) {
      return cnyToUSD(instance + bandwidth, options.env);
    }
  } catch {
    // Coarse static estimate is better than letting usage accounting lie at zero.
  }
  return tencentStaticHourlyPriceUSD(options.serverType, options.env);
}

export function tencentStaticHourlyPriceUSD(
  serverType: string,
  env: Pick<Env, "CRABBOX_TENCENT_CNY_USD_RATE">,
): number | undefined {
  const cny = staticHourlyCNY[serverType];
  return cny === undefined ? undefined : cnyToUSD(cny, env);
}

export async function tencentSpotMaxPrice(
  options: Omit<TencentPricingOptions, "chargeType">,
): Promise<string | undefined> {
  const spot = await tencentHourlyPriceUSD({ ...options, chargeType: "SPOTPAID" }).catch(
    () => undefined,
  );
  if (spot === undefined) {
    return undefined;
  }
  const onDemand = await tencentHourlyPriceUSD({
    ...options,
    chargeType: "POSTPAID_BY_HOUR",
  }).catch(() => undefined);
  const capUSD = onDemand === undefined ? spot * 1.5 : Math.min(spot * 1.5, onDemand);
  const rate = cnyUSDExchangeRate(options.env);
  if (rate <= 0) {
    return undefined;
  }
  return (capUSD / rate).toFixed(4);
}

export async function tencentQuotaPreflightAttempt(
  options: TencentQuotaPreflightOptions,
): Promise<ProvisioningAttempt | undefined> {
  try {
    const quotaType = options.market === "spot" ? "SpotPaidQuotaSet" : "PostPaidQuotaSet";
    const response = await options.call<AccountQuotaResponse>("cvm", "DescribeAccountQuota", {
      AccountQuotaType: quotaType,
    });
    const overview = response.AccountQuotaOverview;
    if (overview?.Region && overview.Region !== options.region) {
      return undefined;
    }
    const quotaSet =
      options.market === "spot"
        ? overview?.AccountQuota?.SpotPaidQuotaSet
        : overview?.AccountQuota?.PostPaidQuotaSet;
    const scoped = (quotaSet ?? []).filter((quota) => !options.zone || quota.Zone === options.zone);
    if (scoped.length === 0) {
      return undefined;
    }
    const exhausted = scoped.every((quota) => quotaRemaining(quota) <= 0);
    if (!exhausted) {
      return undefined;
    }
    const used = scoped.reduce((sum, quota) => sum + (quota.UsedQuota ?? 0), 0);
    const total = scoped.reduce((sum, quota) => sum + (quota.TotalQuota ?? 0), 0);
    return {
      region: options.zone || options.region,
      serverType: options.serverType,
      market: options.market,
      category: "quota",
      message: `tencent quota exhausted for ${instanceFamily(options.serverType)} in ${options.zone || options.region}: ${used}/${total}`,
    };
  } catch {
    return undefined;
  }
}

function tencentPricingPayload(options: TencentPricingOptions): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    Placement: { Zone: options.zone, ProjectId: 0 },
    ImageId: options.imageId,
    InstanceChargeType: options.chargeType,
    InstanceType: options.serverType,
    SystemDisk: {
      DiskType: tencentSystemDiskTypeFor(
        options.config.tencentSystemDiskType ?? "",
        options.env.CRABBOX_TENCENT_SYSTEM_DISK_TYPE,
      ),
      DiskSize: tencentSystemDiskGB(options.config, options.env),
    },
    InternetAccessible: {
      InternetChargeType: "TRAFFIC_POSTPAID_BY_HOUR",
      InternetMaxBandwidthOut: tencentInternetMaxBandwidthOutMbps(options.config, options.env),
      PublicIpAssigned: true,
    },
    InstanceCount: 1,
  };
  if (options.vpcID && options.subnetID) {
    payload["VirtualPrivateCloud"] = { VpcId: options.vpcID, SubnetId: options.subnetID };
  }
  if (options.chargeType === "SPOTPAID") {
    payload["InstanceMarketOptions"] = {
      MarketType: "spot",
      SpotOptions: { SpotInstanceType: "one-time" },
    };
  }
  return payload;
}

function hourlyCNY(price: ItemPrice | undefined): number | undefined {
  const value = price?.UnitPriceDiscount ?? price?.UnitPrice;
  if (value === undefined || !Number.isFinite(value) || value <= 0) {
    return undefined;
  }
  return value;
}

function cnyToUSD(cny: number, env: Pick<Env, "CRABBOX_TENCENT_CNY_USD_RATE">): number {
  return Math.round(cny * cnyUSDExchangeRate(env) * 10_000) / 10_000;
}

function cnyUSDExchangeRate(env: Pick<Env, "CRABBOX_TENCENT_CNY_USD_RATE">): number {
  const parsed = Number.parseFloat(env.CRABBOX_TENCENT_CNY_USD_RATE ?? "");
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0.14;
}

function quotaRemaining(quota: QuotaItem): number {
  if (quota.RemainingQuota !== undefined) {
    return quota.RemainingQuota;
  }
  return (quota.TotalQuota ?? 0) - (quota.UsedQuota ?? 0);
}

function instanceFamily(serverType: string): string {
  return serverType.split(".")[0] || serverType;
}

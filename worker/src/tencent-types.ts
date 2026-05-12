import type { Env, ProvisioningAttempt } from "./types";

export type TencentService = "cvm" | "vpc";
export type TencentChargeType = "POSTPAID_BY_HOUR" | "SPOTPAID";
export type TencentMarket = "on-demand" | "spot";

export interface TencentTag {
  Key: string;
  Value: string;
}

export interface TencentFilter {
  Name: string;
  Values: string[];
}

export interface TencentTagSpecification {
  ResourceType: string;
  Tags: TencentTag[];
}

export interface TencentPlacement {
  Zone: string;
  ProjectId?: number;
}

export interface TencentSystemDisk {
  DiskType?: string;
  DiskSize?: number;
}

export interface TencentInternetAccessible {
  InternetChargeType?: string;
  InternetMaxBandwidthOut?: number;
  PublicIpAssigned?: boolean;
}

export interface TencentVirtualPrivateCloud {
  VpcId: string;
  SubnetId: string;
}

export interface TencentRunInstancesPayload {
  InstanceChargeType: TencentChargeType;
  Placement: TencentPlacement;
  InstanceType: string;
  ImageId: string;
  SystemDisk?: TencentSystemDisk;
  VirtualPrivateCloud?: TencentVirtualPrivateCloud;
  InternetAccessible?: TencentInternetAccessible;
  InstanceCount: number;
  InstanceName: string;
  LoginSettings?: { KeyIds?: string[]; Password?: string };
  SecurityGroupIds?: string[];
  UserData?: string;
  ClientToken: string;
  TagSpecification?: TencentTagSpecification[];
  InstanceMarketOptions?: {
    MarketType: "spot";
    SpotOptions: {
      MaxPrice?: string;
      SpotInstanceType: "one-time";
    };
  };
}

export interface TencentRunInstancesResponse {
  InstanceIdSet?: string[];
  RequestId?: string;
}

export interface TencentInstance {
  Placement?: { Zone?: string; ProjectId?: number };
  InstanceId?: string;
  InstanceType?: string;
  InstanceName?: string;
  InstanceChargeType?: TencentChargeType | string;
  PublicIpAddresses?: string[] | null;
  PrivateIpAddresses?: string[] | null;
  ImageId?: string;
  InstanceState?: string;
  Tags?: TencentTag[] | null;
}

export interface TencentDescribeInstancesResponse {
  TotalCount?: number;
  InstanceSet?: TencentInstance[];
  RequestId?: string;
}

export interface TencentImage {
  ImageId?: string;
  OsName?: string;
  ImageType?: string;
  CreatedTime?: string;
  ImageName?: string;
  ImageDescription?: string;
  ImageState?: string;
  Platform?: string;
  IsSupportCloudinit?: boolean;
  Tags?: TencentTag[] | null;
}

export interface TencentDescribeImagesResponse {
  ImageSet?: TencentImage[];
  TotalCount?: number;
  RequestId?: string;
}

export interface TencentCreateImageResponse {
  ImageId?: string;
  RequestId?: string;
}

export interface TencentKeyPair {
  KeyId?: string;
  KeyName?: string;
  PublicKey?: string;
  Tags?: TencentTag[] | null;
}

export interface TencentDescribeKeyPairsResponse {
  TotalCount?: number;
  KeyPairSet?: TencentKeyPair[];
  RequestId?: string;
}

export interface TencentImportKeyPairResponse {
  KeyId?: string;
  RequestId?: string;
}

export interface TencentZoneInfo {
  Zone?: string;
  ZoneName?: string;
  ZoneState?: string;
}

export interface TencentDescribeZonesResponse {
  TotalCount?: number;
  ZoneSet?: TencentZoneInfo[];
  RequestId?: string;
}

export interface TencentInstanceTypeQuotaItem {
  Zone?: string;
  InstanceType?: string;
  InstanceChargeType?: string;
  InstanceFamily?: string;
  Status?: string;
  StatusCategory?: string;
}

export interface TencentDescribeZoneInstanceConfigInfosResponse {
  InstanceTypeQuotaSet?: TencentInstanceTypeQuotaItem[];
  RequestId?: string;
}

export interface TencentSharedInfra {
  vpcID: string;
  subnetID: string;
  securityGroupID: string;
}

export interface TencentCaller {
  <T>(service: TencentService, action: string, payload: Record<string, unknown>): Promise<T>;
}

export interface TencentPricingOptions {
  call: TencentCaller;
  env: Env;
  region: string;
  zone: string;
  serverType: string;
  config: {
    tencentSystemDiskGB?: number | undefined;
    tencentSystemDiskType?: string | undefined;
    tencentInternetMaxBandwidthOutMbps?: number | undefined;
  };
  chargeType: TencentChargeType;
  imageId: string;
  vpcID?: string;
  subnetID?: string;
}

export interface TencentQuotaPreflightOptions {
  call: TencentCaller;
  region: string;
  zone?: string;
  serverType: string;
  market: TencentMarket;
}

export interface TencentQuotaPreflightResult {
  attempt?: ProvisioningAttempt;
}

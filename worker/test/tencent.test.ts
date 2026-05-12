import { describe, expect, it } from "vitest";

import {
  leaseConfig,
  tencentInstanceTypeCandidatesForTargetClass,
  tencentInternetMaxBandwidthOutMbps,
  tencentRegionCandidates,
} from "../src/config";
import {
  isRetryableTencentProvisioningError,
  tencentInstanceName,
  tencentLabelsFromTags,
  tencentProvisioningErrorCategory,
  tencentTagsFromLabels,
} from "../src/tencent";
import { tc3SignedHeaders } from "../src/tencent-signing";

const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest crabbox@example";

describe("tencent config", () => {
  it("maps classes and regions deterministically", () => {
    expect(tencentInstanceTypeCandidatesForTargetClass("linux", "beast")).toEqual([
      "S5.LARGE16",
      "SA3.LARGE16",
      "S6.LARGE16",
      "S5.2XLARGE16",
    ]);
    expect(
      tencentRegionCandidates(
        { tencentRegion: "ap-hongkong", capacityRegions: ["ap-tokyo"], class: "beast" },
        { CRABBOX_TENCENT_REGION: "ap-singapore", CRABBOX_CAPACITY_REGIONS: "ap-jakarta" },
        "ap-seoul",
      ),
    ).toEqual([
      "ap-seoul",
      "ap-hongkong",
      "ap-singapore",
      "ap-jakarta",
      "ap-tokyo",
      "na-siliconvalley",
    ]);
  });

  it("allows linux and native Windows but rejects macOS fast", () => {
    expect(leaseConfig({ provider: "tencent", sshPublicKey: publicKey }).serverType).toBe(
      "S5.LARGE16",
    );
    expect(
      leaseConfig({ provider: "tencent", target: "windows", sshPublicKey: publicKey }).workRoot,
    ).toBe("C:\\crabbox");
    expect(() =>
      leaseConfig({ provider: "tencent", target: "macos", sshPublicKey: publicKey }),
    ).toThrow("tencent target=macos is not supported");
  });

  it("defaults public IP bandwidth to a non-zero value but preserves explicit zero", () => {
    expect(tencentInternetMaxBandwidthOutMbps({}, {})).toBe(5);
    expect(tencentInternetMaxBandwidthOutMbps({ tencentInternetMaxBandwidthOutMbps: 0 }, {})).toBe(
      0,
    );
    expect(
      tencentInternetMaxBandwidthOutMbps({}, { CRABBOX_TENCENT_INTERNET_BANDWIDTH_MBPS: "0" }),
    ).toBe(0);
  });
});

describe("tencent TC3 signing", () => {
  it("matches the frozen CVM DescribeInstances vector", async () => {
    const signed = await tc3SignedHeaders(
      "AKIDEXAMPLE",
      "SECRETKEYEXAMPLE",
      "cvm",
      "cvm.tencentcloudapi.com",
      "ap-guangzhou",
      "DescribeInstances",
      "2017-03-12",
      '{"Limit":1,"Offset":0}',
      new Date(1551113065 * 1000),
    );

    expect(signed.timestamp).toBe("1551113065");
    expect(signed.authorization).toBe(
      "TC3-HMAC-SHA256 Credential=AKIDEXAMPLE/2019-02-25/cvm/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=85012cd3b8d3013fcf321cc0f864999dc7d0838b1d5e643a7d40b21a9f29775f",
    );
  });
});

describe("tencent labels and errors", () => {
  it("sanitizes tags and round-trips non-reserved labels", () => {
    const tags = tencentTagsFromLabels({
      crabbox: "true",
      owner: "alice@example.com",
      "tencent:reserved": "drop-me",
      "bad key": "bad value!",
      Name: "crabbox-box",
    });
    expect(tags.some((tag) => tag.Key.startsWith("tencent"))).toBe(false);
    expect(tencentLabelsFromTags(tags)).toMatchObject({
      crabbox: "true",
      owner: "alice_example.com",
      bad_key: "bad_value",
      Name: "crabbox-box",
    });
  });

  it("classifies retryable and fatal provisioning failures", () => {
    expect(tencentProvisioningErrorCategory("ResourceInsufficient.SpecifiedInstanceType: no")).toBe(
      "capacity",
    );
    expect(
      isRetryableTencentProvisioningError("ResourceInsufficient.AvailabilityZoneSoldOut"),
    ).toBe(true);
    expect(tencentProvisioningErrorCategory("LimitExceeded.UserAccountQuota: no")).toBe("quota");
    expect(isRetryableTencentProvisioningError("AuthFailure.SecretIdNotFound: no")).toBe(false);
  });

  it("truncates Tencent instance names to the documented limit", () => {
    const name = tencentInstanceName("cbx_abcdef123456", "x".repeat(200));
    expect(name.startsWith("crabbox-cbx_abcdef123456-")).toBe(true);
    expect(name.length).toBeLessThanOrEqual(128);
  });
});

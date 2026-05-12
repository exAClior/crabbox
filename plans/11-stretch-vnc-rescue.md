# 11 — Stretch capabilities

These extend the Worker beyond AWS/Azure parity. **Do not block the primary
acceptance criterion on any of these.** Land them only after milestones M0–M5
in `plans/00-overview.md` are done.

Each item is short on purpose; flesh out when the work is queued.

## 1. Native VNC URL (`DescribeInstanceVncUrl`)

Tencent CVM exposes a built-in NoVNC URL through `DescribeInstanceVncUrl`.
`crabbox vnc` / `crabbox webvnc` today bootstrap their own VNC server inside
the instance. With this API we can offer:

- A faster `--quick-vnc` path that uses the Tencent-issued URL directly.
- A debugging fallback when the bootstrap-installed VNC server is broken.

Implementation sketch:

- New `TencentCVMClient.describeInstanceVncUrl(instanceID) → { url, expiresAt }`.
- Coordinator exposes this on a new lease subroute, e.g.
  `/v1/leases/<id>/vnc/native`, gated by `provider === "tencent"`.

## 2. Password reset (`ResetInstancesPassword`)

Useful for Windows leases where the bootstrap-injected password drifts.

- `TencentCVMClient.resetPassword(instanceID, password, force=false)`.
- Not wired to any CLI command yet; expose internally and document.

## 3. Rescue mode (`EnterRescueMode` / `ExitRescueMode`)

Tencent-specific: lets us repair a stuck instance without re-launching.
Surface as an admin-only `crabbox admin tencent rescue <id>` if and when
the operations team asks for it.

## 4. TAT-driven remote commands

Tencent's TAT service (`tat.tencentcloudapi.com`) lets us run shell or
PowerShell commands on a CVM instance through the control plane, without
SSH. Useful as a fallback when SSH is blocked. Out of scope until a real
need exists; the existing SSH path is the primary contract.

## 5. Disk resize (`ResizeInstanceDisks`)

Allow `crabbox cache` to grow a cached snapshot's system disk on subsequent
warmups without recreating the instance.

## 6. Disaster Recovery Groups, HPC clusters, CHC

The CVM SDK exposes `CreateDisasterRecoverGroup`, `CreateHpcCluster`,
`DescribeChcHosts`, etc. Not relevant to ephemeral test runners. Listed for
completeness; no plan to implement.

## 7. Per-region price refresh

`hourlyPriceUSD` currently calls `InquiryPriceRunInstances` synchronously
per lookup. A small cache (per region × instance type × charge type, TTL
10 minutes) would reduce coordinator load. Same pattern AWS uses for spot
price.

## 8. CLI direct-mode CVM

After HAI is retired (see [plan 12](12-retire-hai.md)), the CLI's
`--provider tencent` only routes through the Worker. AWS and Azure each
have both a Worker path and a CLI direct-mode path (see
`internal/cli/aws.go`, `internal/cli/azure.go`). If we ever want a
CLI-direct CVM backend for parity, this is where it would live. Explicitly
**out of scope** for the current task — flagged here so it's not lost.

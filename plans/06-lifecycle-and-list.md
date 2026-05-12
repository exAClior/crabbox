# 06 — Lifecycle, list, tags, error classification

## Goal

Implement the non-launch methods of `CloudProvider`: `listCrabboxServers`,
`deleteServer`, and the surrounding tag round-trip + error classification
helpers. Plus power lifecycle helpers exposed to `stop` / future ops.

Lives in `worker/src/tencent.ts` (methods on `TencentCVMClient`). Tag/label
helpers and error classification are exported separately so tests can
import them without instantiating the client.

## Methods

### `listCrabboxServers()`

- `DescribeInstances` with `Filters=[{Name:"tag:crabbox",Values:["true"]}]`
  paged by `Offset/Limit` (100 per page). Stop when `TotalCount` is reached.
  `tag-key=crabbox` only proves the key exists; it would include instances not
  owned by Crabbox if someone used a different value.
- Map each `Instance` to `ProviderMachine` using `instanceToMachine` (new
  helper, analogous to `aws.ts` version). Use `Tags` array (with `Key`/`Value`)
  to derive labels via `tencentLabelsFromTags`.
- Any path that describes by explicit IDs (`waitForServerIP`, status/inspect,
  delete validation, image-cache follow-up) must chunk `InstanceIds` into
  batches of at most 100. The SDK docs cap `DescribeInstances.InstanceIds` at
  100 and forbid combining `InstanceIds` with `Filters`.

### `deleteServer(instanceID)`

- `TerminateInstances` with `InstanceIds=[id]`. Treat
  `InvalidInstanceId.NotFound` as success.
- No separate volume cleanup: pay-as-you-go CVM with `SystemDisk` set on
  RunInstances is terminated together. Confirm during smoke test (it's a
  documented Tencent default; the AWS-style "delete on termination" flag is
  the default, not a parameter).

### Lifecycle helpers (used by `crabbox stop` and future commands)

- `stopInstance(id, mode)`:
  - `mode="soft"` → `StopInstances` with `StoppedMode="KEEP_CHARGING"`.
  - `mode="dealloc"` → `StopInstances` with
    `StoppedMode="STOP_CHARGING"` (saves money; needs a VPC+pub-IP setup
    that can survive a stop).
- `startInstance(id)` → `StartInstances`.
- `rebootInstance(id, force=false)` → `RebootInstances` with
  `StopType` if force.

These are not part of the `CloudProvider` interface today, but expose them
as plain methods so `fleet.ts` can wire `crabbox stop --keep` through.

## Tag round-trip

```ts
export function tencentTagsFromLabels(labels: Record<string,string>): Array<{Key:string;Value:string}>;
export function tencentLabelsFromTags(tags: Array<{Key:string;Value:string}>): Record<string,string>;
```

Mirror `azureTagsFromLabels` / `azureLabelsFromTags`:

- Sanitize keys and values to Tencent's allowed pattern (alphanumerics,
  `.`, `_`, `-`, max 127 chars for key, 255 for value).
- Drop reserved prefixes (`tencent:`, `aws:`, etc.).
- Preserve the `Name` tag — Tencent treats it as the instance display name.

## Error classification

```ts
export function tencentProvisioningErrorCategory(message: string): string;
export function isRetryableTencentProvisioningError(message: string): boolean;
```

Tencent errors carry codes like:

| Code                                                | Category    | Retryable? |
| --------------------------------------------------- | ----------- | ---------- |
| `InternalError` / `InternalServerError`             | transient   | yes        |
| `RequestLimitExceeded`                              | throttling  | yes        |
| `ResourceInsufficient.SpecifiedInstanceType`        | capacity    | yes (next instance type) |
| `ResourceInsufficient.AvailabilityZoneSoldOut`      | capacity    | yes (next zone)          |
| `InvalidParameterValue.*`                           | invalid     | no         |
| `AuthFailure.*`                                     | auth        | no         |
| `UnauthorizedOperation.*`                           | auth        | no         |
| `LimitExceeded.UserAccountQuota*`                   | quota       | no         |
| `OperationDenied.*`                                 | policy      | no         |

Map these to the `ProvisioningAttempt.category` strings already used by AWS
(`region`, `capacity`, `quota`, `auth`, `policy`, `transient`) so the
observability layer treats them uniformly.

## `crabbox stop` and `crabbox cleanup` mapping

- `stop --release` → coordinator calls `deleteLeaseServer(lease)` →
  `provider("tencent").deleteServer(...)`. Already wired by `plans/09`.
- `stop --keep` → call `stopInstance(id, "dealloc")` and keep the lease
  record alive. Out of scope for the primary milestone but worth a
  `TODO(tencent)` in the code.
- `cleanup` → `listCrabboxServers()` + filter by lease tags →
  `deleteServer(id)` for each.

## Acceptance

- Round-trip test: create a `Record<string,string>` of labels, convert to
  Tencent `Tags`, convert back, and assert keys/values match (modulo
  sanitization).
- Error category test: a fake response with each of the codes above maps to
  the expected category and retry behavior.
- Manual: launch an instance, see it appear in `crabbox list`, then
  terminate it via `crabbox stop --release`.

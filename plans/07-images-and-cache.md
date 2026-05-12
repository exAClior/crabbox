# 07 — Custom images: `crabbox cache` and `crabbox image`

## Goal

Implement `createImage` and `getImage` on the Tencent provider so the
existing `crabbox cache` (image-cache flow in `fleet.ts`) and `crabbox image`
(`docs/commands/image.md`) commands work against Tencent CVM.

This is a place where Tencent CVM is **richer than Azure**: Azure throws
"azure images are not supported", but CVM has first-class user-image APIs,
so we should implement them. Match AWS shape.

## Tencent API actions

| Step                          | Action                            |
| ----------------------------- | --------------------------------- |
| Snapshot a running instance   | `CreateImage`                     |
| Poll status                   | `DescribeImages`                  |
| Delete user-image             | `DeleteImages`                    |
| Share with another account    | `ModifyImageSharePermission` (stretch) |

Lives in `worker/src/tencent.ts` (methods on `TencentCVMClient`).

## `createImage(instanceID, name, noReboot)`

1. Call `CreateImage` with:
   ```json
   {
     "InstanceId": "ins-...",
     "ImageName": "<name>",
     "ImageDescription": "Crabbox cached image for <slug>",
     "ForcePoweroff": "<noReboot ? "FALSE" : "TRUE">",
     "Reboot": "<noReboot ? "FALSE" : "TRUE">",
     "TagSpecification": [
       { "ResourceType": "image",
         "Tags": [
           {"Key":"crabbox","Value":"true"},
           {"Key":"created_by","Value":"crabbox"}
         ]
       }
     ]
   }
   ```
   Tencent returns `ImageId` directly when the request is accepted.
2. Return a `ProviderImage` immediately with `state: "pending"`. The
   coordinator's poll loop (`fleet.ts` already calls `getImage` on a timer)
   will pick up `state: "available"` later.

## `getImage(imageID)`

- `DescribeImages` with `ImageIds=[imageID]`.
- Map `ImageState` → `ProviderImage.state` (`"CREATING"|"NORMAL"|"USING"|…` →
  `"pending"|"available"|"in-use"|…`). Mirror the strings AWS uses.

## Image cache integration

This does **not** work automatically today. The current Worker image path is
AWS-only: `createImage` rejects `lease.provider !== "aws"` and then calls
`this.provider("aws", lease.region).createImage(...)`; `imageRoute` also calls
`this.provider("aws").getImage(...)`, `validImageID` only accepts `ami-...`,
and promoted-image storage is keyed as `image:aws:promoted`.

Required fleet fixes before claiming cache/image support:

- `cache build` / `POST /v1/images` → resolve the lease, accept providers that
  implement images (`aws`, `tencent`), and dispatch with
  `this.provider(lease.provider, lease.region).createImage(...)`. Preserve the
  existing 400 for Azure/GCP/Hetzner.
- `validImageID` must accept Tencent `img-...` IDs as well as AWS `ami-...`.
- `imageRoute` (`GET /v1/images/:id`, promote/delete when wired) needs a
  provider discriminator: query/body `provider`, a stored image record, or a
  provider-keyed route. Do not infer Tencent solely from `img-...` if another
  provider can use the same prefix later.
- `cache list` → `listCrabboxServers` filtered for `image-cache` tag, plus
  `listImages` (new helper) filtered by tag.
- `cache delete` → `DeleteImages` via the Tencent provider.

## `listImages` helper

Not part of `CloudProvider` today (AWS implements it inline). Add a
TypeScript method on `TencentCVMClient` so future surface can `await
client.listCrabboxImages()` without rebuilding the filter logic.

## Acceptance

- `crabbox cache build --provider tencent` produces a `img-...` ID and
  reaches `state: available` within Tencent's typical 10-15 minute window;
  a regression test proves the Worker dispatches on `lease.provider` and does
  not call the AWS provider for Tencent leases.
- `crabbox image list --provider tencent` lists Crabbox-tagged images.
- `crabbox image delete <id> --provider tencent` removes it; second delete
  returns "not found" gracefully.

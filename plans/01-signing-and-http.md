# 01 — Signing and HTTP client

## Goal

Sign Tencent Cloud API requests from a Cloudflare Worker using
TC3-HMAC-SHA256, and expose a small `tencentCall(service, action, payload)`
function that all subsequent code uses.

## Why this is its own plan

TC3 signing is delicate enough to break silently. Doing it once, behind a
small surface, with deterministic tests, makes every later plan a thin layer.

## Scope

- New module: `worker/src/tencent-signing.ts`. Public exports:
  - `tc3SignedHeaders(secretID, secretKey, service, host, region, action, version, payloadJSON, now)`
    → `{ authorization, timestamp }`
  - `tencentCall<T>(env, opts)` where `opts` carries
    `{ service, host, version, action, region, payload, timeoutMs? }`
  - `class TencentAPIError extends Error { code; requestID; httpStatus }`
- Uses **WebCrypto** (`crypto.subtle.digest`, `crypto.subtle.importKey`,
  `crypto.subtle.sign`) — no Node `crypto` import, no extra dependency.
- One small retry layer on transport errors (`429`, `5xx`, fetch failure)
  with exponential backoff and an absolute timeout (default 30 s).
- Errors thrown as a `TencentAPIError` with `code`, `message`, `requestID`,
  and the original `Response.Error.Code` so callers can string-match like
  `aws.ts` does (`InvalidKeyPair.NotFound` style).

## Reference

- Reference implementation (will be deleted in [plan 12](12-retire-hai.md);
  capture vectors before then): `internal/cli/tencent.go` →
  `tencentSignRequest`, `tencentSHA256Hex`, `hmacSHA256`, `c.do`.
- AWS analog in this repo: `aws4fetch` consumed inside `EC2SpotClient`.
- Azure analog: `AzureClient.token()` + bearer header in `arm()`.

## Key Tencent specifics

- All CVM calls hit `https://cvm.tencentcloudapi.com/`, host header
  `cvm.tencentcloudapi.com`, service `cvm`, version `2017-03-12`.
- VPC calls hit `https://vpc.tencentcloudapi.com/`, service `vpc`, version
  `2017-03-12`.
- Headers required on every request: `X-TC-Action`, `X-TC-Version`,
  `X-TC-Timestamp`, `X-TC-Region`, `Content-Type: application/json`, and the
  `Authorization` header containing the TC3 signature.
- `X-TC-Token` is only needed if the credentials come from STS. Out of scope
  for now; design the helper to accept an optional `token` and pass through.
- Payload is JSON, signed verbatim. The signed body must be the exact bytes
  sent.

## Canonical request layout (TC3-HMAC-SHA256)

```
HTTPRequestMethod = "POST"
CanonicalURI      = "/"
CanonicalQueryString = ""
CanonicalHeaders  = "content-type:application/json; charset=utf-8\nhost:<host>\nx-tc-action:<action-lowercased>\n"
SignedHeaders     = "content-type;host;x-tc-action"
HashedRequestPayload = lower(hex(sha256(payload)))

StringToSign =
  "TC3-HMAC-SHA256\n"
  + <unix-timestamp> + "\n"
  + <yyyy-mm-dd> + "/" + <service> + "/tc3_request\n"
  + lower(hex(sha256(canonicalRequest)))

SecretDate    = HMAC_SHA256("TC3" + secretKey, date)
SecretService = HMAC_SHA256(SecretDate, service)
SecretSigning = HMAC_SHA256(SecretService, "tc3_request")
Signature     = lower(hex(HMAC_SHA256(SecretSigning, StringToSign)))

Authorization = "TC3-HMAC-SHA256 Credential=<secretID>/<date>/<service>/tc3_request, "
                "SignedHeaders=content-type;host;x-tc-action, Signature=<sig>"
```

## Tests (see `plans/10`)

- **Determinism**: given fixed `(secretID, secretKey, action, payload, now)`
  the helper must produce a known-good `Authorization` header. Cross-check
  with the Go CLI (`internal/cli/tencent.go`) for one or two known vectors.
- **Header order independence**: signature must be stable regardless of how
  we pass non-signed headers.
- **Error parsing**: a JSON body of
  `{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"…"},"RequestId":"…"}}`
  becomes a `TencentAPIError` carrying that code.

## Open questions

- Do we need STS token support for federated tenants? Default **no** for
  now — note as a stretch if a real user needs it.
- Should `tencentCall` retry on `RequestLimitExceeded` automatically?
  Default **yes**, with the same backoff as `5xx`. Mirror
  `isRetryableAWSProvisioningError` rules.

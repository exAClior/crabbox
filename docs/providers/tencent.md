# Tencent Cloud HAI

Tencent is a direct SSH lease provider for Tencent Cloud HAI. It creates a HAI
instance through `RunInstances`, waits for a public IP, then treats the box as a
normal Linux SSH target for Crabbox sync and run.

## Status

Usable, with one non-negotiable constraint: HAI does not expose the CVM-style
cloud-init, password, or SSH-key injection path Crabbox uses on other clouds.
The application image must already contain the SSH public key and a usable Linux
user.

The current recommended setup is a custom HAI application image in Singapore
(`ap-singapore`) with:

- an SSH user, for example `crabbox` or `runner`;
- `~/.ssh/authorized_keys` containing the public key matching your local private
  key;
- SSH listening on port `22` or the configured `CRABBOX_SSH_PORT`;
- `rsync`, `git`, `curl`, `jq`, `bash`, `ca-certificates`, and OpenSSH server;
- a writable work root, either `/work/crabbox` owned by the SSH user or
  `CRABBOX_WORK_ROOT` set to a user-writable path.

Application IDs are region-scoped. If the image exists in Singapore, use the
Tencent API region code `ap-singapore`; aliases and misspellings are rejected.

## Environment Setup

```sh
export CRABBOX_PROVIDER=tencent
export CRABBOX_TENCENT_SECRET_ID=...
export CRABBOX_TENCENT_SECRET_KEY=...
export CRABBOX_TENCENT_REGION=ap-singapore
export CRABBOX_TENCENT_APPLICATION_ID=app-xxxxxxxx

export CRABBOX_SSH_USER=runner
export CRABBOX_SSH_KEY="$HOME/.ssh/crabbox-hai"
export CRABBOX_SSH_PORT=22
export CRABBOX_WORK_ROOT=/home/runner/crabbox
```

If your image already creates `/work/crabbox` and runs SSH on Crabbox's default
port/fallback setup, `CRABBOX_SSH_PORT` and `CRABBOX_WORK_ROOT` are optional.
For a plain Linux image, set them as above.

## Smoke Test

```sh
crabbox doctor --provider tencent
crabbox warmup --provider tencent --class standard --idle-timeout 15m
crabbox run --provider tencent --id <slug-or-hai-id> --no-sync -- 'hostname && whoami'
crabbox stop --provider tencent <slug-or-hai-id>
```

Use `--type XL`, `--type 24GB_A`, `--type 3XL`, or `--type 4XL` when you want an
exact HAI bundle. Otherwise use Crabbox classes and let the provider choose the
class mapping.

## Failure Modes

- `Auth failed` / SSH timeout: the image does not have the matching public key,
  the user is wrong, the private key path is wrong, or SSH is not listening.
- `ApplicationId` not found: the app ID is not in `ap-singapore`, or the wrong
  Tencent account/region is configured.
- Sync fails at `rsync` or `git`: the image is too bare. Bake the tools into the
  image; HAI will not receive Crabbox bootstrap user data after creation.

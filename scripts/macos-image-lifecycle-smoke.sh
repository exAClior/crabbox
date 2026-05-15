#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CRABBOX_BIN="${CRABBOX_BIN:-$ROOT/bin/crabbox}"

region="${CRABBOX_MACOS_REGION:-eu-west-1}"
instance_type="${CRABBOX_MACOS_TYPE:-mac2.metal}"
image_name="${CRABBOX_MACOS_IMAGE_NAME:-crabbox-macos-arm64-$(date -u +%Y%m%d-%H%M)}"
ttl="${CRABBOX_MACOS_TTL:-2h}"
idle_timeout="${CRABBOX_MACOS_IDLE_TIMEOUT:-30m}"
image_wait_timeout="${CRABBOX_MACOS_IMAGE_WAIT_TIMEOUT:-60m}"
host_wait_timeout="${CRABBOX_MACOS_HOST_WAIT_TIMEOUT:-5h}"
host_wait_interval="${CRABBOX_MACOS_HOST_WAIT_INTERVAL:-2m}"
allocate="${CRABBOX_MACOS_ALLOCATE:-0}"
run_existing="${CRABBOX_MACOS_RUN:-0}"
create_image="${CRABBOX_MACOS_CREATE_IMAGE:-1}"
promote="${CRABBOX_MACOS_PROMOTE:-0}"
open_webvnc="${CRABBOX_MACOS_OPEN_WEBVNC:-0}"
keep_lease="${CRABBOX_MACOS_KEEP_LEASE:-0}"
release_host="${CRABBOX_MACOS_RELEASE_HOST:-0}"
artifact_root="${CRABBOX_MACOS_ARTIFACT_DIR:-$ROOT/.crabbox/macos-image-smoke/$image_name}"

source_lease=""
candidate_lease=""
promoted_lease=""
allocated_host=""
host_allocated_by_script=0

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  "$@"
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 2
  fi
}

stop_lease() {
  local lease="$1"
  [[ -n "$lease" ]] || return 0
  run "$CRABBOX_BIN" stop --provider aws --target macos "$lease" || true
}

cleanup() {
  if [[ "$keep_lease" == "1" ]]; then
    return 0
  fi
  stop_lease "$promoted_lease"
  stop_lease "$candidate_lease"
  stop_lease "$source_lease"
}
trap cleanup EXIT

lease_from_log() {
  node -e '
const fs = require("fs");
const text = fs.readFileSync(process.argv[1], "utf8");
for (const line of text.trim().split(/\n/).reverse()) {
  try {
    const json = JSON.parse(line);
    if (json.leaseId) {
      console.log(json.leaseId);
      process.exit(0);
    }
  } catch {}
}
process.exit(1);
' "$1"
}

duration_seconds() {
  local value="$1"
  local number
  case "$value" in
    *h) number="${value%h}";;
    *m) number="${value%m}";;
    *s) number="${value%s}";;
    *) number="$value";;
  esac
  if [[ ! "$number" =~ ^[0-9]+$ ]]; then
    printf 'invalid duration: %s\n' "$value" >&2
    exit 2
  fi
  case "$value" in
    *h) printf '%s\n' "$((number * 3600))";;
    *m) printf '%s\n' "$((number * 60))";;
    *s | *) printf '%s\n' "$number";;
  esac
}

mac_host_state() {
  local host="$1"
  "$CRABBOX_BIN" admin mac-hosts list --region "$region" --type "$instance_type" --json |
    jq -r --arg host "$host" '[.[] | select(.id == $host) | .state][0] // empty'
}

wait_for_host_available() {
  local host="$1"
  local label="$2"
  [[ -n "$host" ]] || return 0
  local timeout_seconds interval_seconds deadline state
  timeout_seconds="$(duration_seconds "$host_wait_timeout")"
  interval_seconds="$(duration_seconds "$host_wait_interval")"
  deadline="$(($(date +%s) + timeout_seconds))"
  printf 'waiting for EC2 Mac Dedicated Host %s to become available after %s lease stop; timeout=%s interval=%s\n' "$host" "$label" "$host_wait_timeout" "$host_wait_interval"
  while true; do
    state="$(mac_host_state "$host")"
    if [[ "$state" == "available" ]]; then
      printf 'host %s is available\n' "$host"
      return 0
    fi
    if [[ "$(date +%s)" -ge "$deadline" ]]; then
      printf 'timed out waiting for EC2 Mac Dedicated Host %s to become available; last state=%s\n' "$host" "${state:-missing}" >&2
      return 1
    fi
    printf 'host %s state=%s; sleeping %ss\n' "$host" "${state:-missing}" "$interval_seconds"
    sleep "$interval_seconds"
  done
}

warmup_macos() {
  local label="$1"
  shift
  local log
  log="$(mktemp)"
  printf 'warming macOS lease: %s\n' "$label" >&2
  (
    "$CRABBOX_BIN" warmup \
      --provider aws \
      --target macos \
      --type "$instance_type" \
      --market on-demand \
      --desktop \
      --ttl "$ttl" \
      --idle-timeout "$idle_timeout" \
      --timing-json \
      "$@"
  ) > >(tee -a "$log" >&2) 2> >(tee -a "$log" >&2)
  lease_from_log "$log"
}

smoke_macos_lease() {
  local lease="$1"
  local label="$2"
  local out_dir="$artifact_root/$label"
  # shellcheck disable=SC2016
  run "$CRABBOX_BIN" run \
    --provider aws \
    --target macos \
    --id "$lease" \
    --no-sync \
    --shell -- \
    'set -euo pipefail
     echo macos-smoke-ok
     sw_vers
     command -v ssh
     command -v git
     command -v rsync
     command -v curl
     command -v nc
     test -d "$HOME/crabbox"
     test -w "$HOME/crabbox"
     sudo test -s /var/db/crabbox/vnc.password
     nc -z 127.0.0.1 5900'

  if [[ "$open_webvnc" == "1" ]]; then
    run "$CRABBOX_BIN" webvnc daemon start --provider aws --target macos --id "$lease" --open
  else
    run "$CRABBOX_BIN" webvnc daemon start --provider aws --target macos --id "$lease"
  fi
  sleep 3
  run "$CRABBOX_BIN" webvnc status --provider aws --target macos --id "$lease"
  run "$CRABBOX_BIN" artifacts collect \
    --provider aws \
    --target macos \
    --id "$lease" \
    --output "$out_dir" \
    --screenshot \
    --doctor \
    --webvnc-status \
    --json
}

need node
need jq
if [[ ! -x "$CRABBOX_BIN" ]]; then
  printf 'CRABBOX_BIN is not executable: %s\n' "$CRABBOX_BIN" >&2
  exit 2
fi

printf 'macOS lifecycle smoke region=%s type=%s image=%s host-wait=%s\n' "$region" "$instance_type" "$image_name" "$host_wait_timeout"
run "$CRABBOX_BIN" admin mac-hosts offerings --region "$region" --type "$instance_type"
hosts_json="$("$CRABBOX_BIN" admin mac-hosts list --region "$region" --type "$instance_type" --json)"
printf '%s\n' "$hosts_json" | jq .

existing_host="$(
  printf '%s\n' "$hosts_json" |
    jq -r --arg type "$instance_type" '[.[] | select(.instanceType == $type and .state == "available") | .id][0] // empty'
)"

if [[ -n "$existing_host" ]]; then
  if [[ "$run_existing" != "1" && "$allocate" != "1" ]]; then
    printf 'available EC2 Mac Dedicated Host found: %s\n' "$existing_host"
    printf 'set CRABBOX_MACOS_RUN=1 to use the existing host and continue.\n'
    exit 0
  fi
  printf 'using existing EC2 Mac Dedicated Host: %s\n' "$existing_host"
  allocated_host="$existing_host"
else
  dry_log="$(mktemp)"
  run "$CRABBOX_BIN" admin mac-hosts allocate --region "$region" --type "$instance_type" --dry-run | tee "$dry_log"
  if ! grep -q '^dry-run ok ' "$dry_log"; then
    printf 'macOS lifecycle blocked before paid work: EC2 Mac host dry-run did not succeed.\n' >&2
    exit 1
  fi

  if [[ "$allocate" != "1" ]]; then
    printf 'dry-run passed; set CRABBOX_MACOS_ALLOCATE=1 to allocate a paid EC2 Mac Dedicated Host and continue.\n'
    exit 0
  fi

  allocate_log="$(mktemp)"
  run "$CRABBOX_BIN" admin mac-hosts allocate --region "$region" --type "$instance_type" --force | tee "$allocate_log"
  allocated_host="$(awk '/^allocated host=/{for (i=1; i<=NF; i++) if ($i ~ /^host=/) { sub(/^host=/, "", $i); print $i; exit }}' "$allocate_log")"
  host_allocated_by_script=1
fi

if [[ -n "$allocated_host" ]]; then
  export CRABBOX_AWS_MAC_HOST_ID="$allocated_host"
fi

source_lease="$(warmup_macos source)"
smoke_macos_lease "$source_lease" source

if [[ "$create_image" != "1" ]]; then
  printf 'source lease smoke passed; set CRABBOX_MACOS_CREATE_IMAGE=1 to create the AMI.\n'
  exit 0
fi

image_json="$("$CRABBOX_BIN" image create --id "$source_lease" --name "$image_name" --no-reboot=false --wait --wait-timeout "$image_wait_timeout" --json)"
printf '%s\n' "$image_json" | jq .
ami_id="$(printf '%s\n' "$image_json" | jq -r '.id // .image.id // empty')"
if [[ -z "$ami_id" ]]; then
  printf 'image create did not return an AMI id\n' >&2
  exit 1
fi

stop_lease "$source_lease"
source_lease=""
wait_for_host_available "$allocated_host" source

candidate_lease="$(CRABBOX_AWS_AMI="$ami_id" warmup_macos candidate)"
smoke_macos_lease "$candidate_lease" candidate

if [[ "$promote" != "1" ]]; then
  printf 'candidate AMI smoke passed: %s\n' "$ami_id"
  printf 'set CRABBOX_MACOS_PROMOTE=1 to promote it and run the promoted-image smoke.\n'
  exit 0
fi

run "$CRABBOX_BIN" image promote "$ami_id" --target macos --region "$region" --json
stop_lease "$candidate_lease"
candidate_lease=""
wait_for_host_available "$allocated_host" candidate

promoted_lease="$(warmup_macos promoted)"
smoke_macos_lease "$promoted_lease" promoted
printf 'promoted macOS image lifecycle passed: %s\n' "$ami_id"

if [[ "$release_host" == "1" && -n "$allocated_host" ]]; then
  if [[ "$host_allocated_by_script" != "1" && "${CRABBOX_MACOS_RELEASE_EXISTING_HOST:-0}" != "1" ]]; then
    printf 'refusing to release pre-existing EC2 Mac Dedicated Host %s; set CRABBOX_MACOS_RELEASE_EXISTING_HOST=1 to confirm.\n' "$allocated_host" >&2
    exit 1
  fi
  stop_lease "$promoted_lease"
  promoted_lease=""
  wait_for_host_available "$allocated_host" promoted
  run "$CRABBOX_BIN" admin mac-hosts release "$allocated_host" --region "$region" --force
fi

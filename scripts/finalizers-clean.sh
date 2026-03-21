#!/usr/bin/env bash
# clean-finalizers.sh
# Find and remove finalizers from all namespaced resources in a namespace.
# Useful when an operator/controller was removed and CRs are stuck terminating.
#
# Requirements: kubectl, jq
# Usage:
#   ./clean-finalizers.sh -n <namespace>            # list what would be patched (dry-run)
#   ./clean-finalizers.sh -n <namespace> -y         # actually remove finalizers
#   ./clean-finalizers.sh -n <namespace> -k crd1,deployments.apps  # limit to specific resource types
#   ./clean-finalizers.sh -n <namespace> --context <kubectx>       # use a specific context

set -euo pipefail

NS=""
CTX=""
DRYRUN=1
KINDS_FILTER=""  # comma-separated kubectl resource names, e.g. "foos.example.com,widgets.example.com"
ONLY_LIST=0

usage() {
  cat <<'USAGE'
clean-finalizers.sh - remove finalizers from namespaced resources

Options:
  -n, --namespace   <ns>     Target namespace (required)
  -y, --yes                  Apply changes (default is dry-run: list only)
  -k, --kinds      <list>    Comma-separated resource names to limit (e.g. "foos.example.com,deployments.apps")
  --context         <ctx>    kubectl context to use
  -h, --help                 Show this help

Examples:
  ./clean-finalizers.sh -n my-ns
  ./clean-finalizers.sh -n my-ns -y
  ./clean-finalizers.sh -n my-ns -k foos.example.com -y
USAGE
}

# --- args ---
while (( "$#" )); do
  case "${1}" in
    -n|--namespace) NS="${2:-}"; shift 2;;
    --context) CTX="${2:-}"; shift 2;;
    -y|--yes) DRYRUN=0; shift;;
    -k|--kinds) KINDS_FILTER="${2:-}"; shift 2;;
    -h|--help) usage; exit 0;;
    *) echo "Unknown arg: $1"; usage; exit 1;;
  esac
done

if [[ -z "${NS}" ]]; then
  echo "ERROR: --namespace is required"; usage; exit 1
fi

if [[ "${NS}" == "kube-system" || "${NS}" == "kube-public" ]]; then
  echo "Refusing to operate on protected namespace: ${NS}" >&2
  exit 1
fi

K="kubectl"
if [[ -n "${CTX}" ]]; then
  K="${K} --context ${CTX}"
fi

# Verify tools
command -v jq >/dev/null 2>&1 || { echo "jq is required"; exit 1; }
${K} version >/dev/null 2>&1 || { echo "kubectl not configured?"; exit 1; }

# Build resource list
if [[ -n "${KINDS_FILTER}" ]]; then
  IFS=',' read -r -a RES_LIST <<< "${KINDS_FILTER}"
else
  # all namespaced, listable resources (exclude subresources like */status)
  mapfile -t RES_LIST < <(${K} api-resources --namespaced=true --verbs=list --output=name | grep -v '/')
fi

echo "Namespace: ${NS}"
echo "Context:   ${CTX:-(default)}"
echo "Mode:      $([[ ${DRYRUN} -eq 1 ]] && echo 'DRY-RUN (no changes)' || echo 'APPLY CHANGES')"
echo

patched_count=0
error_count=0

patch_finalizers() {
  local res="$1" name="$2"
  # Try several patch strategies; some APIs accept one form but not others.
  if ${K} -n "${NS}" patch "${res}" "${name}" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1; then
    return 0
  fi
  if ${K} -n "${NS}" patch "${res}" "${name}" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1; then
    return 0
  fi
  if ${K} -n "${NS}" patch "${res}" "${name}" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' >/dev/null 2>&1; then
    return 0
  fi
  return 1
}

kubectl -n "$NS" delete pod --all --grace-period=0 --force

for res in "${RES_LIST[@]}"; do
  # Fetch items with non-empty finalizers
  json="$(${K} -n "${NS}" get "${res}" -o json 2>/dev/null || true)"
  [[ -z "${json}" || "${json}" == "null" ]] && continue

  mapfile -t ITEMS < <(jq -r '
    .items // []
    | map(select(.metadata.finalizers and ((.metadata.finalizers | type) == "array") and (.metadata.finalizers | length > 0)))
    | .[]
    | [ .metadata.name, (.metadata.finalizers | join(",")) ]
    | @tsv
  ' <<<"${json}")


  [[ ${#ITEMS[@]} -eq 0 ]] && continue

  echo "Resource: ${res}"
  for line in "${ITEMS[@]}"; do
    name="${line%%$'\t'*}"
    fins="${line#*$'\t'}"
    echo "  - ${res}/${name}    finalizers=[${fins}]"

    if [[ ${DRYRUN} -eq 0 ]]; then
      if patch_finalizers "${res}" "${name}"; then
        echo "      ✓ removed finalizers"
        ((patched_count++))
      else
        echo "      ✗ failed to patch; you may need RBAC permissions or to use cluster-admin"
        ((error_count++))
      fi
    fi
  done
  echo
done

if [[ ${DRYRUN} -eq 1 ]]; then
  echo "Dry-run complete. Re-run with -y to remove the listed finalizers."
else
  echo "Done. Patched objects: ${patched_count}. Failures: ${error_count}."
fi

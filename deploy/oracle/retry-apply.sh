#!/bin/bash
# Vault: retry the Resource Manager stack's Apply job until Ampere A1
# capacity frees up in the region. Free to run - Resource Manager itself
# doesn't charge, and a failed "Out of host capacity" job never provisions
# anything, so nothing bills.
#
# Prereq: OCI CLI installed (https://docs.oracle.com/iaas/Content/API/SDKDocs/cliinstall.htm)
# and configured via `oci setup config` (needs an API key - Console: Profile
# icon -> My Profile -> API Keys -> Add API Key).
set -uo pipefail  # no -e: a failed `oci` call (e.g. 429) must fall through to the check below, not kill the loop

STACK_ID="ocid1.ormstack.oc1.ap-batam-1.amaaaaaaajpslfyareuun5vk2qmklbulzfetsw3vzlq3zjhwbmquecsox4ca"  # Console: Resource Manager -> Stacks -> vault-server-stack -> Copy OCID
CAPACITY_RETRY=120      # wait between attempts after a normal capacity failure
RATE_LIMIT_BACKOFF=300  # starting wait after a 429; doubles each consecutive 429
MAX_BACKOFF=1800

while true; do
  echo "$(date): starting apply job..."
  JOB_ID=$(oci resource-manager job create-apply-job \
    --stack-id "$STACK_ID" \
    --execution-plan-strategy AUTO_APPROVED \
    --query 'data.id' --raw-output)

  if [[ -z "$JOB_ID" ]]; then
    echo "$(date): rate limited creating job - waiting ${RATE_LIMIT_BACKOFF}s"
    sleep "$RATE_LIMIT_BACKOFF"
    RATE_LIMIT_BACKOFF=$(( RATE_LIMIT_BACKOFF * 2 < MAX_BACKOFF ? RATE_LIMIT_BACKOFF * 2 : MAX_BACKOFF ))
    continue
  fi
  RATE_LIMIT_BACKOFF=300  # reset after a successful job creation

  STATE="ACCEPTED"
  while [[ "$STATE" == "ACCEPTED" || "$STATE" == "IN_PROGRESS" ]]; do
    sleep 5
    STATE=$(oci resource-manager job get --job-id "$JOB_ID" --query 'data."lifecycle-state"' --raw-output)
  done

  if [[ "$STATE" == "SUCCEEDED" ]]; then
    echo "$(date): instance created. Stopping."
    break
  fi

  echo "$(date): job ended in $STATE (likely still out of capacity) - retrying in ${CAPACITY_RETRY}s"
  sleep "$CAPACITY_RETRY"
done

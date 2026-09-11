# Vault: retry the Resource Manager stack's Apply job until Ampere A1
# capacity frees up in the region. Free to run - Resource Manager itself
# doesn't charge, and a failed "Out of host capacity" job never provisions
# anything, so nothing bills.
#
# Prereq: OCI CLI installed and configured via `oci setup config`.

$StackId = "ocid1.ormstack.oc1.ap-batam-1.amaaaaaaajpslfyareuun5vk2qmklbulzfetsw3vzlq3zjhwbmquecsox4ca"
$CapacityRetrySeconds = 120  # wait between attempts after a normal capacity failure
$RateLimitBackoff = 300      # starting wait after a 429; doubles each consecutive 429
$MaxBackoff = 1800

while ($true) {
    Write-Host "$(Get-Date): starting apply job..."
    $jobId = oci resource-manager job create-apply-job `
        --stack-id $StackId `
        --execution-plan-strategy AUTO_APPROVED `
        --query "data.id" --raw-output

    if ([string]::IsNullOrWhiteSpace($jobId)) {
        Write-Host "$(Get-Date): rate limited creating job - waiting ${RateLimitBackoff}s"
        Start-Sleep -Seconds $RateLimitBackoff
        $RateLimitBackoff = [Math]::Min($RateLimitBackoff * 2, $MaxBackoff)
        continue
    }
    $RateLimitBackoff = 300  # reset after a successful job creation

    $state = "ACCEPTED"
    while ($state -eq "ACCEPTED" -or $state -eq "IN_PROGRESS") {
        Start-Sleep -Seconds 5
        $state = oci resource-manager job get --job-id $jobId --query data.`"lifecycle-state`" --raw-output
    }

    if ($state -eq "SUCCEEDED") {
        Write-Host "$(Get-Date): instance created. Stopping."
        break
    }

    Write-Host "$(Get-Date): job ended in $state (likely still out of capacity) - retrying in ${CapacityRetrySeconds}s"
    Start-Sleep -Seconds $CapacityRetrySeconds
}

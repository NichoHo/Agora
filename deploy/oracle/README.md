# Vault: Oracle Cloud "Always Free" Deployment

Single instance running the full Vault stack via Docker Compose, on Oracle Cloud
Infrastructure's (OCI) permanent free tier. This is the free-forever sibling of
[`deploy/terraform`](../terraform/README.md) (the AWS EC2 version, ~$15/month).

This doc is written as a brief for another model or a person to turn into exact
click-by-click steps. It states facts and constraints (verified against Oracle's
docs, 2026-08-20); it does not assume the reader knows OCI's console layout.

---

## Why this costs $0, and how to keep it that way

- **The instance itself is free forever**, not a 12-month trial like AWS/GCP.
  Oracle's "Always Free" resources remain free for the life of the account, no
  expiration.
- **A new account starts in a 30-day/$300 trial.** During the trial, do not
  provision anything that is not explicitly labeled "Always Free" in the
  Console — trial resources are billed against the credit and are removed
  (not silently charged) once the trial ends, unless the account is manually
  upgraded.
- **The one hard rule: never click "Upgrade Account."** Oracle cannot charge
  the card on file unless the account is explicitly converted from
  Always-Free/Trial to Pay-As-You-Go. Sign-up puts a $1 authorization hold on
  the card for identity verification; Oracle reverses it immediately (the
  bank may take longer to reflect the reversal). That hold is the only
  contact this deployment ever makes with the card.
- **Set a spending budget as a tripwire anyway** (belt and suspenders): OCI
  Console -> Governance & Administration -> Cost Management -> Budgets ->
  create a budget of $1 with an alert at 0.01%. It can't prevent a charge
  since none can happen on an unupgraded account, but it emails a warning if
  anything unexpected shows up on the account.

## Resource budget (verified against `docs.oracle.com/iaas` on 2026-08-20)

Oracle cut the Ampere A1 Always Free allowance in half on 2026-06-15. Current limits:

| Resource | Always Free allowance |
|---|---|
| Ampere A1 compute (ARM, `VM.Standard.A1.Flex`) | 2 OCPU + 12 GB RAM total, pooled across the tenancy |
| AMD compute (`VM.Standard.E2.1.Micro`) | 2 instances, 1/8 OCPU + 1 GB RAM each — too small for this stack, ignore |
| Block storage | 200 GB total (boot + data volumes combined) |
| Outbound data transfer | 10 TB/month |
| Load balancer | 1 flexible LB, 10 Mbps — not needed, see "No TLS" below |

**Use one Ampere A1 instance sized 2 OCPU / 12 GB RAM.** That's the entire ARM
pool in a single box — don't split it into two smaller instances, this stack
needs it as one Docker Compose host. 12 GB is generous headroom for
Postgres + Redpanda + 4 Go services + Next.js + the Python `assist` service;
the boot volume (default 50 GB) leaves 150 GB of the storage pool unused.

## The two gotchas that aren't about money

1. **"Out of host capacity" on instance creation.** Ampere A1 Always Free
   capacity is oversubscribed in most regions and creation often fails on the
   first few tries. This is a known, widely-reported OCI issue, not a
   mistake in the steps. Retry across the different Availability Domains the
   Console offers, and retry at different times of day — it usually succeeds
   within a day of retries, sometimes on the first try.
2. **Idle-instance reclamation.** Oracle may reclaim (stop and delete) an
   Always Free compute instance if, over any 7-day window, its 95th-percentile
   CPU, network, *and* memory utilization all stay under 20%. A portfolio demo
   that sits untouched for a week risks this. It costs nothing to lose (the
   instance is free to recreate), but it does mean the demo link could go
   dark. Two options: check on it periodically yourself, or add a cron job
   that does a few minutes of trivial CPU work daily (`crontab -e` on the
   instance: `0 9 * * * timeout 300 yes > /dev/null`) to keep utilization
   readings above the floor. This is a widely used community workaround, not
   an Oracle-documented guarantee.

## No TLS, matching the AWS deploy

Like `deploy/terraform`, this is a demo-grade deploy: HTTP only, no reverse
proxy, no certificate. The web app is reached directly at
`http://<public-ip>:3001`. Skipped here on purpose to match the existing AWS
setup — add Caddy or the free OCI load balancer with a Let's Encrypt cert
later if the portfolio link needs to be `https://`.

---

## Steps

### 1. Create the account

Sign up at [oracle.com/cloud/free](https://www.oracle.com/cloud/free/). Requires
a credit card (identity verification only, see above) and, in some countries,
a phone number/ID. Pick a home region close to you — Always Free compute can
only be provisioned in the home region chosen at signup, it cannot be changed
later.

### 2. Create the VCN first, separately from the instance

Do not use the instance wizard's inline "Create new virtual cloud network"
option — it's known to create a subnet that the "Automatically assign public
IPv4 address" toggle then refuses with *"You must select a public subnet to
assign a public IPv4 address"*, even though the subnet is labeled public.
Side-step it entirely:

Console -> hamburger menu -> Networking -> Virtual Cloud Networks ->
**Start VCN Wizard** -> **Create VCN with Internet Connectivity** -> Next,
using the default name/CIDR. This one-shot wizard wires up a real public
subnet with an Internet Gateway and route table already attached, so the
public-IP toggle works when the instance references it.

### 3. Create the compute instance

Console -> hamburger menu -> Compute -> Instances -> **Create Instance**.

- **Name:** `vault-server`
- **Image:** Canonical Ubuntu, latest 22.04 (or newer) **aarch64** build — the
  Console defaults to this once the shape below is picked. Do not pick a
  Marketplace image; only the plain platform image is guaranteed free.
- **Shape:** Change Shape -> Ampere -> `VM.Standard.A1.Flex` -> set
  **2 OCPUs / 12 GB memory**. The Console shows "Always Free eligible" next
  to the shape when the sliders are within budget — confirm that badge is
  present before continuing.
- **Networking:** choose **"Select existing virtual cloud network"**, pick
  the VCN created in step 2, then its public subnet. Turn on "Automatically
  assign public IPv4 address" — it should switch on cleanly now.
- **Add SSH keys:** either "Generate a key pair for me" (Oracle shows a
  one-time download prompt for the private key right after — save it, it
  can't be retrieved again) or upload/paste a public key you already have
  (`~/.ssh/id_ed25519.pub`).
- **Boot volume:** leave at the 50 GB default.
- **Advanced options -> Management -> Cloud-init script:** paste the full
  contents of [`cloud-init.sh`](cloud-init.sh) from this directory.

Click **Create**. If it fails with "Out of host capacity," see the gotcha
above and retry (change the Availability Domain shown in the shape config
before retrying).

Also confirm the region selector (top of Console) is your **home region**
from step 1 before clicking Create — Always Free compute only provisions
there, in any other region it isn't free.

### 4. Open the cloud-level firewall (Security List)

The instance's own iptables is opened by `cloud-init.sh`, but OCI has a
second firewall in front of it. Console -> Networking -> Virtual Cloud
Networks -> (the VCN just created) -> Security Lists -> Default Security
List -> **Add Ingress Rules**:

| Source CIDR | Protocol | Destination Port |
|---|---|---|
| `0.0.0.0/0` | TCP | 22 (usually already present) |
| `0.0.0.0/0` | TCP | 3001 |

Only 22 and 3001 are opened — `id`/`market`/`pay`/`assist` (8081-8084) and
Postgres/Redpanda (5432/9092) stay unreachable from the internet, same as the
AWS deploy.

### 5. Point the app at its public IP, then start it

`docker-compose.yml`'s `WEB_URL` (on `id`) and `WEB_ORIGIN` (on `web`) read
from a `${PUBLIC_HOST:-localhost}` variable — they're sent to the *browser*
for redirects and origin checks, so they must resolve to the instance's real
public IP, not `localhost`. Compose reads that variable from a `.env` file in
the project directory, which is why this is a manual step: OCI's instance
metadata service doesn't expose the public IP the way AWS's does (it's a
NAT'd address attached to the VNIC, not the instance), so `cloud-init.sh`
can't fill this in automatically.

Find the public IP on the instance's Console page, then:

```bash
ssh ubuntu@<public-ip>
cd ~/vault
echo "PUBLIC_HOST=<public-ip>" > .env
docker compose up -d
```

First run builds 5 images from source (Go x4, Python, Next.js) — several
minutes on a fresh instance. `docker compose ps` should show every service
healthy/running when done.

### 6. Verify

Open `http://<public-ip>:3001` in a browser. Confirm login/signup works end
to end (this exercises the `id` service's OIDC redirect through `WEB_URL`,
the part that breaks if step 5 was skipped).

## Teardown

Console -> Compute -> Instances -> `vault-server` -> **Terminate** (also tick
"Permanently delete the boot volume" to free the storage quota). No cost was
ever incurred, so there's nothing to "clean up" billing-wise — this just
reclaims the free-tier slot.

## Not included (intentionally, matches the AWS deploy's stance)

- TLS/HTTPS, custom domain
- Backups, multi-AZ, managed Postgres
- Secrets management (`.env` values are the same dev defaults as local)
- Terraform/IaC for OCI — this doc uses the Console + cloud-init directly
  because OCI's Terraform provider needs API signing keys and several OCIDs
  gathered by hand, which is more setup than a single free instance
  justifies. Worth doing later if the portfolio specifically wants
  Terraform-on-OCI as a talking point — ask if that's wanted.

Sources: [Oracle Always Free Resources](https://docs.oracle.com/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm), [Oracle Cloud Free Tier docs](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier.htm), [InfoQ: Oracle halves Free Tier Ampere A1 limits](https://www.infoq.com/news/2026/07/oracle-cloud-free-tier-limits/)

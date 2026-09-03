#!/bin/bash
# Vault: Oracle Cloud "Always Free" bootstrap script.
# Paste this into the instance's "Cloud-init script" box during creation
# (Advanced options -> Management -> Paste cloud-init script). Runs once, as root, on first boot.
set -euo pipefail

# --- Open the OS firewall ---------------------------------------------------
# Oracle's Ubuntu images ship with iptables rules that block everything but
# SSH by default. The OCI Security List (configured in the Console) is a
# SEPARATE firewall in front of this one - both must allow a port.
apt-get update -y
apt-get install -y iptables-persistent
iptables -I INPUT -p tcp --dport 80 -j ACCEPT
iptables -I INPUT -p tcp --dport 443 -j ACCEPT
iptables -I INPUT -p tcp --dport 3001 -j ACCEPT
netfilter-persistent save

# --- Install Docker + Compose plugin (Docker's official apt repo) ----------
apt-get install -y ca-certificates curl gnupg git
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update -y
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
usermod -aG docker ubuntu

# --- Clone the repo ----------------------------------------------------------
# Does NOT start the stack yet - the id/web services bake WEB_URL/WEB_ORIGIN
# into redirect and cookie checks, and those must point at this instance's
# public IP, which isn't known until after the instance exists. Finish with
# the two commands at the bottom of deploy/oracle/README.md after boot.
cd /home/ubuntu
git clone https://github.com/NichoHo/Agora.git agora
chown -R ubuntu:ubuntu agora

# ponytail: single instance, no ASG/ALB/RDS. Swap in ECS+RDS if the demo needs HA.

# ---------------------------------------------------------------------------
# Agora: EC2 instance
# ---------------------------------------------------------------------------

# ── Latest Amazon Linux 2023 AMI ─────────────────────────────────────────────

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

# ── EC2 Instance ─────────────────────────────────────────────────────────────

resource "aws_instance" "agora" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  key_name               = var.key_name
  vpc_security_group_ids = [aws_security_group.agora.id]
  subnet_id              = data.aws_subnets.default.ids[0]

  associate_public_ip_address = true

  root_block_device {
    volume_size = 30
    volume_type = "gp3"
  }

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail

    # ── Install Docker ───────────────────────────────────────────────────
    dnf update -y
    dnf install -y docker git
    systemctl enable --now docker
    usermod -aG docker ec2-user

    # ── Install Docker Compose plugin ────────────────────────────────────
    mkdir -p /usr/local/lib/docker/cli-plugins
    COMPOSE_VERSION=$(curl -s https://api.github.com/repos/docker/compose/releases/latest | grep '"tag_name"' | head -1 | cut -d'"' -f4)
    curl -SL "https://github.com/docker/compose/releases/download/$${COMPOSE_VERSION}/docker-compose-linux-x86_64" \
      -o /usr/local/lib/docker/cli-plugins/docker-compose
    chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

    # ── Clone repo & start services ──────────────────────────────────────
    cd /home/ec2-user
    git clone ${var.repo_url} agora
    chown -R ec2-user:ec2-user agora
    cd agora

    # WEB_URL/WEB_ORIGIN are sent to the browser for redirects and origin
    # checks, so they must resolve to this instance's public IP, not
    # localhost. EC2's metadata service can tell us that directly.
    TOKEN=$(curl -sX PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
    PUBLIC_IP=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/public-ipv4)
    echo "PUBLIC_HOST=$PUBLIC_IP" > .env
    chown ec2-user:ec2-user .env

    docker compose up -d
  EOF

  tags = {
    Name    = "agora-server"
    Project = "agora"
  }
}

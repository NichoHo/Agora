# Agora: Terraform Deployment

Single EC2 instance running the full Agora stack via Docker Compose.

## Prerequisites

- [Terraform ≥ 1.9](https://developer.hashicorp.com/terraform/downloads)
- AWS CLI configured (`aws configure`) with credentials that can manage EC2, VPC, and security groups
- An **EC2 key pair** created in your target region (needed for SSH access)

## Deploy

```bash
cd deploy/terraform

terraform init
terraform apply -var="key_name=YOUR_KEY_PAIR_NAME"
```

Terraform will output the instance's **public IP**, **DNS**, and a ready-to-click **web URL** on port 3001.

## Cost

A `t3.small` instance in `us-east-1` with a 30 GB gp3 volume runs **~$15/month** (on-demand pricing). Use a spot instance or reserved instance to reduce cost further.

## Security Note

> **This configuration is designed for demo / development use.**
>
> - SSH (port 22) is open to `0.0.0.0/0`. Restrict it to your IP in production.
> - No TLS termination. Add an ALB + ACM certificate for HTTPS.
> - No database backups or multi-AZ. Swap in RDS if you need durability.
> - Secrets are not managed. Use AWS Secrets Manager or SSM Parameter Store in production.

## Teardown

```bash
terraform destroy -var="key_name=YOUR_KEY_PAIR_NAME"
```

# ---------------------------------------------------------------------------
# Vault: input variables
# ---------------------------------------------------------------------------

variable "region" {
  description = "AWS region to deploy into"
  type        = string
  default     = "us-east-1"
}

variable "key_name" {
  description = "Name of the EC2 key pair for SSH access"
  type        = string
}

variable "repo_url" {
  description = "Git repository URL to clone onto the instance"
  type        = string
  default     = "https://github.com/NichoHo/Agora.git"
}

variable "instance_type" {
  description = "EC2 instance type"
  type        = string
  default     = "t3.small"
}

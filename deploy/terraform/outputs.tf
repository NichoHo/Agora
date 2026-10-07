# ---------------------------------------------------------------------------
# Agora: outputs
# ---------------------------------------------------------------------------

output "public_dns" {
  description = "Public DNS of the Agora EC2 instance"
  value       = aws_instance.agora.public_dns
}

output "public_ip" {
  description = "Public IP of the Agora EC2 instance"
  value       = aws_instance.agora.public_ip
}

output "web_url" {
  description = "URL to access the Agora web app"
  value       = "http://${aws_instance.agora.public_dns}:3001"
}

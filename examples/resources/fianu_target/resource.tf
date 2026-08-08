# A target is a concrete cloud destination bound to one or more environments.
# `environments` takes a path or a UUID; paths resolve server-side at deploy.

resource "fianu_target" "eks_prod" {
  path = "targets.eks_prod"
  name = "EKS Production"

  detail = {
    description    = "Primary production Kubernetes cluster."
    cloud_provider = "AWS"
    type           = "kubernetes"
    service        = "eks"
    region         = "us-east-1"
    tags           = ["prod", "pci"]
  }

  environments = [
    { environment = fianu_environment.production.path },
  ]
}

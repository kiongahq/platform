# Amazon EKS deployment

EKS is the first staging target. Use managed EC2 node groups across at least two
Availability Zones, with three Ready nodes minimum. Kionga's Helm chart is
provider-neutral; `deploy/helm/providers/eks.example.yaml` selects an EBS class.
It is **not** an EKS Auto Mode recipe (that mode uses a different CSI provisioner).

1. Create or select an EKS cluster, private subnets and managed node groups. Install
   and verify the VPC CNI, CoreDNS, metrics-server and AWS EBS CSI add-on. Enable
   VPC CNI NetworkPolicy explicitly; it is not implied by the NetworkPolicy API.
   Enable cluster OIDC and associate the IAM OIDC provider used by IRSA. The
   OIDC issuer for IRSA is separate from the application's login IdP.
2. Configure a scoped IRSA role for `kionga-secret-reader` in `kionga-system`.
   Allow only `secretsmanager:GetSecretValue` and `DescribeSecret` for the
   production secret ARN. Apply the reviewed
   `deploy/helm/secretstore-aws.example.yaml` with real account, role and region.
   The External Secrets Operator itself must be installed separately.
3. Install and test an ingress controller, wildcard DNS and TLS certificates for
   both console and workspace hosts. Apply the EBS StorageClass example after
   checking CSI IAM and KMS permissions. EBS volumes are zonal/RWO: use shared
   object storage for project data, not a PVC shared by many workspaces.
4. Provision redundant RDS PostgreSQL (TLS `verify-full`), Redis, S3-compatible
   storage, Kafka plus Kafka REST, and selected ML engines. Put credentials in
   Secrets Manager. Configure VPC egress, security groups, KMS, backups and alerting.
   The chart does not create these services.

```bash
aws eks update-kubeconfig --name CLUSTER --region REGION --alias kionga-eks
kubectl --context kionga-eks apply -f deploy/helm/providers/eks-storageclass.example.yaml
python3 scripts/cloud-preflight.py eks --context kionga-eks --cluster CLUSTER --region REGION
bash scripts/deploy-kubernetes.sh kionga-eks kionga-system /secure/production.yaml deploy/helm/providers/eks.example.yaml
bash scripts/deploy-kubernetes.sh kionga-eks kionga-system /secure/production.yaml deploy/helm/providers/eks.example.yaml --apply
```

Create the namespace, CRDs, SecretStore and cloud dependencies first as described
in [Managed Kubernetes](../managed-kubernetes.md). The preflight is read-only;
it does not prove IAM permissions, policy enforcement, actual EBS provisioning,
or failover. Run the authenticated production verifier and recovery exercises.
Use a short-lived AWS federated deploy role in CI, not a permanent access key.

References: [EKS VPC CNI NetworkPolicy](https://docs.aws.amazon.com/eks/latest/userguide/cni-network-policy-configure.html),
[IRSA OIDC provider](https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html),
[EBS CSI StorageClass](https://docs.aws.amazon.com/eks/latest/userguide/create-storage-class.html),
[External Secrets AWS](https://external-secrets.io/latest/provider/aws-secrets-manager/).

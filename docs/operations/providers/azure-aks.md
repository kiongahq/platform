# Azure Kubernetes Service deployment

Use AKS Linux node pools with three Ready nodes across at least two availability
zones. Enable the OIDC issuer and Microsoft Entra Workload ID for External Secrets.
For new Linux clusters prefer Azure CNI powered by Cilium for NetworkPolicy
enforcement; Azure NPM has a published retirement path. Verify the Azure Disk CSI
driver and its identity permissions.

Federate `kionga-secret-reader` with a narrowly scoped managed identity that can
read the required Key Vault secrets. Review and apply
`deploy/helm/secretstore-azure.example.yaml`. Install External Secrets Operator,
ingress, TLS, wildcard DNS and the platform dependencies described in
[Managed Kubernetes](../managed-kubernetes.md). Azure Disk is zonal/RWO; shared
project data belongs in object storage, not a shared PVC.

```bash
az aks get-credentials --resource-group RESOURCE_GROUP --name CLUSTER --context kionga-aks
kubectl --context kionga-aks apply -f deploy/helm/providers/aks-storageclass.example.yaml
python3 scripts/cloud-preflight.py aks --context kionga-aks --cluster CLUSTER --resource-group RESOURCE_GROUP
bash scripts/deploy-kubernetes.sh kionga-aks kionga-system /secure/production.yaml deploy/helm/providers/aks.example.yaml
bash scripts/deploy-kubernetes.sh kionga-aks kionga-system /secure/production.yaml deploy/helm/providers/aks.example.yaml --apply
```

The preflight checks reported configuration, not effective IAM, policy behavior,
PVC attachment or recovery. Run the authenticated production verifier and
restore/failover drills before admitting users.

References: [AKS Workload ID](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster),
[AKS NetworkPolicy](https://learn.microsoft.com/en-us/azure/aks/use-network-policies),
[Azure Disk CSI](https://learn.microsoft.com/en-us/azure/aks/create-volume-azure-disk),
[External Secrets Azure](https://external-secrets.io/latest/provider/azure-key-vault/).

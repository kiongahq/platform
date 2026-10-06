# Google Kubernetes Engine deployment

Use a GKE Standard cluster with three Ready nodes across at least two zones.
Enable Workload Identity Federation, GKE Dataplane V2 (or a supported enforced
NetworkPolicy provider), and the GCE PD CSI driver. Dataplane V2 has special
`ipBlock` behavior for Pod and Service IP ranges; test Kionga's policies against
the actual VPC and DNS paths before allowing user workloads.

Bind `kionga-secret-reader` to a narrowly scoped Google service account that can
read only the required Secret Manager secret versions. Review and apply
`deploy/helm/secretstore-gcp.example.yaml`. Install External Secrets Operator,
ingress, TLS, wildcard DNS and the platform dependencies described in
[Managed Kubernetes](../managed-kubernetes.md). GCE PD is zonal/RWO; keep shared
project data in object storage.

```bash
gcloud container clusters get-credentials CLUSTER --project PROJECT --location LOCATION
kubectl config current-context # Use this explicit context in every command below.
kubectl --context CONTEXT apply -f deploy/helm/providers/gke-storageclass.example.yaml
python3 scripts/cloud-preflight.py gke --context CONTEXT --cluster CLUSTER --project PROJECT --location LOCATION
bash scripts/deploy-kubernetes.sh CONTEXT kionga-system /secure/production.yaml deploy/helm/providers/gke.example.yaml
bash scripts/deploy-kubernetes.sh CONTEXT kionga-system /secure/production.yaml deploy/helm/providers/gke.example.yaml --apply
```

The preflight checks reported configuration, not effective IAM, policy behavior,
PVC attachment or recovery. Run the authenticated production verifier and
restore/failover drills before admitting users.

References: [GKE NetworkPolicy](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/network-policy),
[Workload Identity Federation](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/workload-identity),
[GCE PD CSI](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/gce-pd-csi-driver),
[External Secrets GCP](https://external-secrets.io/latest/provider/google-secrets-manager/).

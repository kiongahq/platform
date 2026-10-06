# Managed Kubernetes deployment

Use `deploy/helm/kionga` for the primary production deployment. The chart runs
three gateway replicas and two replicas each of the operator, integration worker,
feature gateway, storage proxy and trace proxy. The gateway has an HPA; every
component has a disruption budget, rolling-update policy and topology constraints.
The operator uses leader election and namespace-scoped caches and permissions.
The integration worker uses Kafka consumer groups and reports polling readiness.
These mechanisms support availability; they do not replace redundant dependencies.

Provider-specific setup and storage overlays: [EKS](providers/aws-eks.md),
[GKE](providers/gcp-gke.md), and [AKS](providers/azure-aks.md). AWS EKS is the
initial staging target, but no live cloud deployment is implied by these guides.
Run `scripts/cloud-preflight.py` for the selected provider before Helm.

## 1. Prepare the infrastructure

Provide a supported managed Kubernetes cluster (chart minimum 1.29; use a version
still supported by your provider), preferably three or more nodes across zones.
Use nodes compatible with your published images; do not assume ARM images exist.
Provide the following before installing:

- A NetworkPolicy-enforcing CNI, DNS, metrics-server, and an ingress controller.
- TLS certificate automation or an existing TLS Secret for the public hostname.
- An encrypted, expandable CSI storage class and tested volume snapshots.
- Private, redundant PostgreSQL and Redis, S3-compatible object storage, Kafka
  **and Kafka REST**, plus the MLflow/Prefect/other engines you intend to offer.
- An HTTPS OIDC provider with the client callback `https://HOST/auth/callback`.
- External Secrets Operator supporting `external-secrets.io/v1` when using the
  supplied secret-manager integration. Install it separately using its release guide.
- A Prometheus Operator and kube-state-metrics when enabling `monitoring.enabled`
  as in the production example. Configure Prometheus to select the example
  ServiceMonitor/PrometheusRule labels or change `monitoring.selectorLabels`.

GPU drivers/device plugins, GPU node pools, scheduling policies, engine installs,
database creation, external engine migrations and provider IAM are infrastructure responsibilities.
The chart does not install demo databases or make them highly available for you.

Bootstrap with a cluster-administrator identity, then use a narrower deploy identity:

```bash
kubectl --context CONTEXT create namespace kionga-system
kubectl --context CONTEXT label namespace kionga-system pod-security.kubernetes.io/enforce=restricted
kubectl --context CONTEXT apply -f config/crd/
```

The chart creates a separate `kionga-workloads` namespace with restricted pod
security and a ResourceQuota. Do not pre-create it without handling Helm ownership.
Do not share it between independent releases. It is retained on uninstall to avoid
silently deleting user workloads; retention is not a backup.

For browser workspaces, delegate wildcard DNS such as
`*.workspaces.example.com` to the ingress and provide a wildcard certificate
in `workspaceIngress.tlsSecretName`. Set `workspaceIngress.baseDomain` and
`config.KIONGA_WORKSPACE_BASE_DOMAIN` to the same base. The console and workspace
hosts must share a registrable site, for example `console.example.com` and
`*.workspaces.example.com`, so iframe cookies remain first-party while each
workspace has a distinct origin. The chart routes wildcard hosts to the gateway.
A hostname uses an opaque 32-character workspace hash so long or sensitive OIDC
subjects do not appear in DNS or certificate logs.
A launch issues a 45-second single-use PostgreSQL ticket and redeems it for a
30-minute host-only HttpOnly cookie. Each request checks the current platform
grant and workspace owner. Suspending a grant denies access; console logout
revokes the subject's workspace sessions. Verify ingress Host-header forwarding,
wildcard certificate and browser cookie behavior in staging.
The console submits tickets as form POST bodies to keep them out of request URLs;
do not log POST bodies at the ingress. Jupyter's frame policy is rewritten at the
gateway to permit only the console origin while preserving its other CSP rules.

## 2. Configure identity and secrets

Use an OIDC provider compatible with the gateway's RS256 validation. Set issuer,
JWKS, authorization/token URLs, audience and client ID in the values overlay.
Map controlled identity-provider claims to platform roles; reserve administrator
membership for trusted operators. Provision normal users by immutable OIDC subject.
Email domain membership alone does not automatically grant platform permissions.
See [RBAC](../reference/rbac.md) for actual authorization behavior.

Store these values in the secret manager, not Helm values or command arguments:

| Consumer | Required Secret keys |
| --- | --- |
| Gateway | `DATABASE_URL`, `OIDC_CLIENT_SECRET`, `MLAIOPS_INTERNAL_TOKEN`, `KIONGA_CREDENTIAL_KEY` |
| Feature gateway | `REDIS_URL`, `MLAIOPS_INTERNAL_TOKEN` |
| Storage proxy | `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `MLAIOPS_INTERNAL_TOKEN` |

Use a PostgreSQL URI with `sslmode=verify-full`. For a private CA, extend the
deployment with a read-only CA mount and set `sslrootcert` in the URI to its path.
The UID/GID 65532 service must be able to read it. Keep the credential encryption
key stable across replicas and restarts; it protects persisted personal credentials.

Provider store examples live under `deploy/helm/`:

- AWS: configure IAM role trust and narrow Secrets Manager permissions for the
  referenced Kubernetes service account.
- GCP: bind the Kubernetes service account to a Google identity permitted to read
  only the relevant Secret Manager secret versions.
- Azure: configure the federated identity and Key Vault permissions for the
  referenced workload identity service account.

Replace example account IDs, regions, projects and identities before applying a
store. The examples do not create these IAM bindings. Prefer a namespaced
SecretStore. `production.example.yaml` maps remote JSON properties to per-service
Secrets through ExternalSecret resources. Restrict Secret read access and enable
cluster Secret encryption at rest. Cloud identity for External Secrets does **not**
automatically give the application's S3 client native AWS/GCP/Azure identity support;
the storage proxy currently uses the mounted S3-compatible credentials.

For authenticated Kafka REST add `KAFKA_REST_TOKEN` to both gateway and worker
`secrets` lists and populate their Secrets/ExternalSecrets. The consumer sends bearer authentication
and rejects cross-origin consumer URLs when credentials are configured. Create
the topics required by `scripts/local-topics.sh` through your Kafka administration
process. Configure engine authentication similarly using supported secret-file
keys and per-component secret lists; empty defaults do not configure an engine.

## 3. Build and review a release

Copy `deploy/helm/production.example.yaml` to a release overlay. Replace every
placeholder image with a verified `repository@sha256:DIGEST`, including notebook
and IDE images. The build workflow publishes digest summaries, provenance and
SBOMs; verification/admission policies must be configured by your organization.
Never pass secret values through `helm --set`: Helm retains release values.

Set the storage class, public host, ingress class, TLS Secret and all engine URLs.
The example internal service URLs assume release `kionga` in `kionga-system`;
adjust them if changing those names. Size requests/limits and quotas from measured
workload requirements. Requests affect scheduling; limits are not reserved capacity.

The chart defaults to denied external egress except DNS. Replace the example
documentation CIDR with explicit destinations/ports for the Kubernetes API,
PostgreSQL, Redis, IdP, Kafka REST, object storage and engines. Include your cluster
API's actual address and port. NodeLocal DNS may need a separate policy. Standard
NetworkPolicy does not express FQDN allowlists; use your CNI's supported mechanism
or an egress proxy when destination addresses change.

User workload egress is separately restricted. Add narrowly scoped policies for
approved data/model sources and engines. Do not solve connectivity by globally
allowing every destination. The ingress policy assumes an in-cluster controller
in `ingress.controllerNamespace`; provider load balancers that do not originate
from such pods require a reviewed policy for their actual source addresses.

With monitoring enabled, the chart creates ServiceMonitors for the gateway,
operator and integration worker, plus alerts for low gateway replica count and
stale Kafka polling. Scrape access is limited to `monitoring.namespace` through
NetworkPolicies. Gateway metrics use port 9090; operator metrics use 8080;
worker metrics use `/metrics` on 8086. Ensure the Prometheus namespace has the
expected label and the rules are selected by your Prometheus installation.
Configure alert routing, certificate expiry, database, storage, Kafka lag and
ExternalSecret alerts in your provider monitoring stack; those integrations are
not installed by this chart.

## 4. Validate and deploy

Run from the repository root with Helm and kubectl installed:

```bash
helm lint deploy/helm/kionga -f /path/to/production.yaml --namespace kionga-system --strict
bash scripts/deploy-kubernetes.sh CONTEXT kionga-system /path/to/production.yaml
bash scripts/deploy-kubernetes.sh CONTEXT kionga-system /path/to/production.yaml --apply
kubectl --context CONTEXT -n kionga-system get pods,services,ingress,hpa,pdb
kubectl --context CONTEXT -n kionga-system get externalsecrets
```

The deployment script also accepts provider overlays between the base values file
and `--apply`, preserving Helm's left-to-right values precedence. Use the
provider guide's exact command to select its storage class; review all settings
in the base values first. Preflight and Helm rendering are necessary, not
sufficient: test actual volume provisioning, network-policy enforcement, cloud
identity permissions and ingress TLS in the staging cluster.

With staged admin and normal-user OIDC tokens in protected files, and a project
the user cannot access, run the read-only verifier:

```bash
python3 scripts/verify-production.py \
  --context CONTEXT --namespace kionga-system \
  --origin https://console.example.com \
  --workspace-host https://workbench-0123456789abcdef0123456789abcdef.workspaces.example.com \
  --admin-token-file /secure/admin-id-token \
  --user-token-file /secure/user-id-token \
  --forbidden-project PROJECT_ID --expect-monitoring
```

It checks replicas, digests, probes, HPA, ingress TLS, namespace policies,
multi-zone nodes, HTTPS, anonymous denial, admin/user access, cross-project
denial and isolated-host denial. The tokens are IdP bearer tokens, not Kionga
personal API keys. Do not put tokens on the command line. Add
`--test-workspace-handoff` to create an ephemeral one-time workspace session,
check its host-only cookie and API access, then revoke it. The default check is
read-only. Neither mode replaces engine, restore and failover tests.

The first script invocation renders and checks cluster prerequisites without
installing. `--apply` uses an atomic, waited Helm upgrade. The cluster context is
explicit. Secret synchronization, certificate issuance, external connectivity and
available scheduling capacity must all succeed before admitting users.
Gateway readiness checks the database; worker readiness checks recent successful
polling. Other health probes are not end-to-end engine tests.

Perform every [production acceptance test](production-operations.md) using real
identities and data paths. Replica counts alone do not prove failover works.

## 5. Operate and roll back

Promote reviewed immutable releases through staging. Use protected CI environments
and short-lived cloud OIDC federation or a GitOps controller; this repository does
not configure your cloud deploy identity. Avoid long-lived kubeconfig/cloud keys
in CI secrets. Rotate mounted secrets with a coordinated rollout: processes load
them at startup, not dynamically on file refresh.

```bash
helm history kionga --kube-context CONTEXT -n kionga-system
helm rollback kionga REVISION --kube-context CONTEXT -n kionga-system --wait --timeout 15m
```

Rollback restores manifests, not databases, CRDs, external secrets or user data.
Review migration compatibility before downgrading. Never delete PVCs as a generic
recovery step. See the shared operations guide for backups and rotation.

## Authoritative references

- [Kubernetes Secret practices](https://kubernetes.io/docs/concepts/security/secrets-good-practices/)
- [Pod disruption budgets](https://kubernetes.io/docs/tasks/run-application/configure-pdb/)
- [Network policies](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [Pod security standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
- [External Secrets: AWS](https://external-secrets.io/latest/provider/aws-secrets-manager/), [Google](https://external-secrets.io/latest/provider/google-secrets-manager/), [Azure](https://external-secrets.io/latest/provider/azure-key-vault/)
- [Helm upgrade](https://helm.sh/docs/helm/helm_upgrade/)
- [GitHub Actions cloud OIDC](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-cloud-providers)

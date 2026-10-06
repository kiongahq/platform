# Public hosting

Kionga supports two deployment packages without using repository `.env` files
as the production secret source. Managed Kubernetes is the primary team deployment.

| Path | Intended use | Included |
| --- | --- | --- |
| [Managed Kubernetes](operations/managed-kubernetes.md) | Distributed teams and workload provisioning | Replicated control plane, operator, integration worker, service gateways, namespace isolation, External Secrets integration |
| [Public VM](operations/public-vm.md) | Pilot or gateway in front of existing infrastructure | Digest-pinned gateway and Caddy TLS edge; external identity and database |

Neither package creates your cloud account infrastructure, databases, identity
provider, ML engines, or secret manager. The VM package does not provision
Kubernetes workloads. Kubernetes packaging is locally tested, not a certification
that a particular cloud installation is secure or highly available.

Read the [production operations and acceptance checklist](operations/production-operations.md)
before exposing either path to users. Public same-origin notebook and IDE proxying
is disabled in production by default. Managed Kubernetes can route isolated
workspace subdomains with single-use handoff tickets and host-only sessions;
wildcard DNS, TLS and real cluster acceptance are required.

## Configuration model

Version non-secret JSON or Helm values in Git. Keep credentials in a cloud secret
manager and mount them as files. The Go services read `KIONGA_CONFIG_FILE` and
supported `NAME_FILE` inputs at startup. Explicit environment variables override
JSON, but supplying both a nonempty secret environment variable and its file fails.
Secrets are not accepted in the JSON config. Production gateway startup requires
HTTPS OIDC, a matching callback, PostgreSQL with `sslmode=verify-full`, a strong
internal token, and a 32-byte base64 credential encryption key.

## Development compatibility

`make local-up` remains the development environment. New public deployment uses
`make public-up DEPLOYMENT=/etc/kionga/deployment.json`. The old Compose overlay
is available only through `KIONGA_LEGACY_PUBLIC_DEPLOY=1 bash deploy/public-up.sh`;
it is a legacy demo path, not the recommended production deployment. Do not
expose its development credentials or services to the Internet.

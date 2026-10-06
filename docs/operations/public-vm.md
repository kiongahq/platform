# Public VM deployment without .env

This package runs **the gateway and Caddy only**, using managed or separately
operated identity, PostgreSQL and ML services. It is suitable for a pilot or an
edge gateway, not an HA replacement for the managed Kubernetes workload plane.
Notebook/IDE provisioning requires the Kubernetes path and appropriate routing.

## Prepare the host

Use a maintained Linux host with Docker Engine, Compose plugin and Python 3.
Point a public DNS hostname at it and allow inbound 80/443. Restrict SSH to your
administrators; do not expose database/engine ports. Provide private connectivity
to dependencies and HTTPS egress for identity and ACME. Establish patching, disk
monitoring, backups and a recovery owner before hosting users.

Copy the examples under `deploy/vm/` to an administrator-controlled `/etc/kionga/`:

- `deployment.example.json` becomes `deployment.json`: real hostname, ACME email,
  verified gateway and Caddy image digests, config path and secret directory.
- `config.example.json` becomes `config.json`: non-secret HTTPS OIDC endpoints,
  client/audience, matching public origin and callback, plus service URLs as needed.

Every JSON value in the application configuration must be a string. The launcher
rejects mutable image tags and disables Compose's repository `.env` loading.
It passes only an allowlist of host settings to Docker. Do not put credentials
into either JSON file, Git, cloud-init user data, shell history or command arguments.

## Deliver secret files

Use the VM's cloud identity and your cloud secret agent or configuration management
to materialize these files under `/run/kionga/secrets`:

```text
DATABASE_URL
OIDC_CLIENT_SECRET
MLAIOPS_INTERNAL_TOKEN
KIONGA_CREDENTIAL_KEY
```

This repository does not install a provider-specific agent or grant its IAM role.
Grant that role read access only to the required secrets. Configure atomic file
replacement, retry/alert behavior and service ordering. `/run` is ephemeral: after
reboot, the secret agent must populate files before containers start.

On a conventional rootful Linux Docker host, use root-owned files with group 65532,
mode 0640 and a protected directory (0750); ensure the deploying identity and
container UID/GID 65532 can read the files and config. Rootless/user-namespace
Docker requires matching mapped ownership instead. Compose file secrets are
mounts, not an encrypted secret manager. Do not rely on Compose `uid`/`gid` fields
to change ownership of bind-mounted host files.

Use `sslmode=verify-full` in the PostgreSQL URI. For a private database CA, extend
the Compose service with a read-only CA mount and reference it with `sslrootcert`.
Use independently generated strong client/internal credentials, and a stable
base64-encoded 32-byte encryption key. Back up that key securely with your recovery
plan; replacing it invalidates access to previously encrypted personal credentials.

For optional authenticated engine integrations, explicitly add their secret file
mount and corresponding supported `NAME_FILE` environment variable to the VM
Compose definition. The default package deliberately mounts only four secrets.

## Validate and start

From the repository root:

```bash
python3 deploy/vm/up.py /etc/kionga/deployment.json --validate-only
make public-up DEPLOYMENT=/etc/kionga/deployment.json
```

If `make` is unavailable, run `bash deploy/public-up.sh /etc/kionga/deployment.json`.
Validation checks the deployment inputs and Compose configuration, not cloud IAM
or database credentials. Startup pulls pinned images, recreates services and waits
for the public HTTPS readiness endpoint. Only Caddy publishes host ports; gateway
runs non-root, with a read-only root filesystem and dropped capabilities. Caddy
maintains certificate data in named volumes and requires working DNS/ACME access.

Readiness success is not full acceptance. Test login, provisioning authorization,
engine connectivity and recovery using the [shared checklist](production-operations.md).

## Updates, rotation and shutdown

Update reviewed digests in deployment JSON, materialize new secret versions, then
rerun the start command. It forces recreation so atomically replaced secret files
are remounted. Expect downtime: this is a single gateway deployment. Coordinate
internal-token changes with every dependent service. Roll back by restoring the
previous reviewed digests/configuration and rerunning; this does not undo data
migrations or restore secrets automatically.

```bash
make public-down DEPLOYMENT=/etc/kionga/deployment.json
# Equivalent, retaining certificate/data volumes:
python3 deploy/vm/up.py /etc/kionga/deployment.json --stop
```

Do not use `down -v` as an upgrade or troubleshooting command. Configure host
service ordering for boot and secret delivery; Docker restart policies alone do
not guarantee secrets are present at boot. Back up external databases and retain
the Caddy certificate volume. Do not log secret file contents when diagnosing errors.

## References

- [Docker Compose secrets](https://docs.docker.com/compose/how-tos/use-secrets/)
- [Compose Secret file semantics](https://docs.docker.com/reference/compose-file/secrets/)
- [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https)
- [AWS instance roles](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/iam-roles-for-amazon-ec2.html)
- [Google Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials)
- [Azure managed identities](https://learn.microsoft.com/en-us/entra/identity/managed-identities-azure-resources/overview)

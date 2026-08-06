# Migrating to Kionga identifiers

The Kionga rename is a clean contract migration, not only a visual label change.
Runtime identifiers, workspace commands, browser state, and Kubernetes resource
names use the Kionga vocabulary consistently. No compatibility aliases are retained
for the previous runtime identifiers.

## New canonical identifiers

| Surface | Canonical value |
| --- | --- |
| Workspace command | `kionga` |
| Workspace script | `deploy/workspace/kionga.py` |
| Custom coding-agent command | `KIONGA_CUSTOM_AGENT_COMMAND` |
| Workspace root | `KIONGA_WORKSPACE` |
| Workspace subject | `KIONGA_SUBJECT` |
| Browser/auth cookie prefix | `kionga_` |
| Console preference prefix | `kionga.` |
| Function invocation annotation | `io.kionga.invocation` |
| SDK pipeline document kind | `KiongaPipeline` |
| Kubernetes kinds | `KiongaAgent`, `KiongaPipelineRun`, `KiongaModelPromotion`, `KiongaWorkspace`, `KiongaTool`, `KiongaConnection` |
| Kubernetes resource plurals | `kiongaagents`, `kiongapipelineruns`, `kiongamodelpromotions`, `kiongaworkspaces`, `kiongatools`, `kiongaconnections` |

The API group remains `mlaiops.io/v1alpha1`, and the REST base path remains
`/api/v1`. Repository module paths and `MLAIOPS_*` public configuration remain
unchanged unless listed above.

## Local and single-VM upgrade

1. Back up PostgreSQL and object storage.
2. Update `.env` to the canonical variables above.
3. Rebuild the gateway, workbench, IDE, operator, and integration-worker images.
4. Recreate application containers without deleting volumes.
5. Sign in again; the cookie rename intentionally invalidates earlier sessions.
6. Reapply function trigger settings so persisted annotations use
   `io.kionga.invocation`.
7. Run `kionga agents` in Jupyter and the IDE to verify the installed command.

Browser preferences use a new key and therefore return to defaults. Existing Git
repositories, model artifacts, pipeline records, and object-store data do not need
to be renamed.

## Kubernetes upgrade

Kubernetes kinds and resource plurals are discovery contracts. They cannot be
treated as an in-place text edit on a live cluster.

For a disposable Kind environment, recreate the cluster:

```bash
make kind-down
make kind-up
make test-e2e
```

For a stateful cluster:

1. stop new lifecycle submissions;
2. export all existing platform custom resources and their status for audit;
3. back up PostgreSQL, object storage, Secrets, and persistent volumes;
4. install the Kionga CRDs and matching operator RBAC;
5. transform and apply desired specifications under the new kinds, omitting
   server-generated metadata and status;
6. deploy the Kionga-aware operator and integration worker;
7. verify generated Deployments, Services, PVCs, Secrets, owner references, and
   readiness conditions;
8. remove superseded CRDs only after every desired resource has reconciled and the
   rollback window has closed.

Do not delete a CRD before exporting its objects: Kubernetes deletes the custom
resources stored under that CRD. Use a maintenance window for production clusters.

## Automation and clients

Update shell scripts, notebooks, CI jobs, container commands, API payload annotations,
and `kubectl` queries. Rebuild generated documentation with `mkdocs build --clean` so
stale pages and search-index entries are removed.

Validate the upgrade with:

```bash
make verify
make test-integration
make docs-build
make test-e2e       # when the Kubernetes lane is available
```

Finally, search the working tree and generated site for pre-migration identifiers.
Git history is intentionally not rewritten; historical commits remain immutable.

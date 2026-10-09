# Access control: users, groups, roles and policies

Kionga answers one question on every request: may this principal perform this
action on this resource, right now? This guide explains each building block,
the order they are applied in, and how to grant, restrict and debug access.

## The building blocks

| Concept | What it is | Where it lives | Who manages it |
| --- | --- | --- | --- |
| **Identity** | Who is calling: an OIDC subject (`sub` claim), a local username, a personal API key, or the internal service token. | Identity provider, local accounts, API keys | The identity provider or an administrator |
| **User** | An identity with an administrator-provisioned *access profile*: role, assigned services, assigned projects, storage buckets and compute grant. Unprovisioned identities can sign in but only request access. | `PUT /api/v1/admin/users/{subject}` | Administrators and operators |
| **Group** | A named set of subjects. Policies attached to a group apply to every member. | `/api/v1/admin/iam/groups` | Administrators and operators |
| **Role** | A coarse job function: `admin`, `operator`, `engineer`, `viewer`, `user` (normal user) or `service` (in-platform machine identity). Each role has a built-in *role baseline* policy. | The user's access profile (or OIDC claims for unprovisioned identities) | Administrators |
| **Permission** | One action from the [action catalog](#action-catalog), such as `pipeline:Run`, on a resource such as `kionga:project/prj-a/pipeline/train`. | Policy statements | Policy authors |
| **Service assignment** | Which console services (projects, pipelines, features, workbench, ide, ...) a normal user may use at all. It is the outer boundary for normal users. | The access profile's `services` | Administrators |
| **Policy** | A named, versioned document of statements that allow or deny actions on resources, optionally under conditions. Every save creates an immutable revision. | `/api/v1/admin/iam/policies` | Administrators and operators |
| **Attachment** | Binds a policy to a user or a group. | `/api/v1/admin/iam/attachments` | Holders of `policy:Attach` (anti-escalation applies) |
| **Quota** | Capacity limits from the compute grant: projects, concurrent runs, functions, CPU, memory, GPUs, storage. Quotas are checked after authorization and are never granted by policies. | The access profile's `compute` and `storage` | Administrators |

## Precedence

Every request goes through these steps in order. The first step that refuses
decides.

1. **Authentication.** No valid identity, no access (`401`).
2. **Suspension.** A suspended profile is denied everything.
3. **Coarse gate (role and services).** The role and, for normal users, the
   assigned services decide whether the API surface is open at all. A normal
   user without the `pipelines` service never reaches pipeline handlers, and
   only administrators and operators reach `/api/v1/admin/*`.
4. **Policy evaluation** for the specific action and resource:
    1. **Explicit deny wins.** If any applicable statement denies, the request
       is denied, whatever else allows it.
    2. **Allow.** Otherwise, if any applicable statement allows (from the role
       baseline or an attached policy), the request is allowed.
    3. **Default deny.** Otherwise it is denied.
5. **Quotas and admission.** Allowed requests must still fit the compute,
   storage and count limits of the caller's grant.

Applicable policies are the caller's role baseline, every policy attached to
the caller, and every policy attached to a group the caller belongs to.

!!! note "Nothing changes until you attach a policy"
    Role baselines reproduce the behavior that existed before policies, so a
    deployment with no attachments behaves exactly as before. Normal users are
    limited to the projects assigned to them plus the projects they own;
    engineers and viewers are limited the same way when their access profile
    lists project IDs, and see every project otherwise.

## Role baselines

Baselines are built-in managed policies (`kionga-admin`, `kionga-operator`,
`kionga-engineer`, `kionga-viewer`, `kionga-user`, `kionga-service`). They are
listed in the console's **Policies** tab, cannot be edited or attached, and are
scoped per principal at evaluation time.

| Role | Baseline |
| --- | --- |
| `admin`, `operator` | Every action on every resource. |
| `engineer` | Read projects; create projects; every pipeline action and logs; read and write features; open workspaces. Project-scoped when the profile lists projects. |
| `viewer` | Read projects, pipelines, logs and features. Project-scoped when the profile lists projects. Never writes or opens workspaces. |
| `user` | Only statements for assigned services: `projects` (read in scope, create), `pipelines` (every pipeline action and logs in scope), `features` (read and write), `workbench`/`ide` (open that workspace in scope). |
| `service` | Every action; the coarse gate limits it to reporting paths. |

## Action catalog

`GET /api/v1/iam/catalog` returns this catalog. Actions are case-insensitive;
`*` matches every action and `pipeline:*` every action in a namespace.

| Action | Grants | Enforced at |
| --- | --- | --- |
| `project:Read` | See a project | project list and detail |
| `project:Create` | Create a project (quotas still apply) | `POST /api/v1/projects` |
| `pipeline:Read` | See definitions, revisions and runs | definition and run reads, the `/api/v1/events` digest |
| `pipeline:Write` | Create or change definitions, schedules, rollbacks; report run steps | definition saves, rollback, trigger pause/resume, step reports |
| `pipeline:Run` | Submit, cancel, retry runs | submit, cancel, retry; scheduled runs check the schedule owner |
| `pipeline:OverrideParameters` | Override parameters for one run | submit with `overrides.parameters` |
| `pipeline:OverrideResources` | Override node CPU, memory or GPU | submit with `overrides.nodes.*.resources` |
| `pipeline:OverrideImage` | Override a node's image | submit with `overrides.nodes.*.image` |
| `logs:Read` | Read run logs | logs are removed from run reads when denied |
| `workspace:Open` | Launch JupyterLab or the IDE | `POST /api/v1/workspaces/{kind}/launch` |
| `feature:Read` | See feature views | feature list and the events digest |
| `feature:Write` | Apply feature views, report materializations | `POST /api/v1/features` |
| `secret:Read` | Reserved; the gateway never returns secrets today | not yet enforced |
| `infra:Provision` | Create, test and activate connections | connection writes |
| `blog:Write` | Create, edit and delete blog posts | admin blog writes |
| `blog:Publish` | Publish a blog post | saving a post as `published` |
| `policy:Read` | Read IAM objects; explain another principal | `/api/v1/admin/iam/*` reads, `explain`/`effective` for others |
| `policy:Write` | Create, edit and delete policies and groups | policy and group writes |
| `policy:Attach` | Attach or detach a policy | attachments |
| `user:Manage` | Provision, change, suspend or revoke users; set local passwords; review access requests | `/api/v1/admin/users/*`, access request review |

Models, agents, functions, storage and the catalog are governed by the coarse
gate plus project assignment; they have no policy actions yet.

## Resources

Resource names start with `kionga:` and use `/`-separated segments. IDs are
URL-escaped inside a segment.

| Resource | Example |
| --- | --- |
| `kionga:project/<project>` | `kionga:project/prj-churn` |
| `kionga:project/<project>/pipeline/<pipeline>` | `kionga:project/prj-churn/pipeline/pipe-123` |
| `kionga:project/<project>/workspace/<kind>` | `kionga:project/prj-churn/workspace/workbench` |
| `kionga:workspace/<kind>` | `kionga:workspace/ide` (launch without a project) |
| `kionga:feature/<name>` | `kionga:feature/customer_profile` |
| `kionga:user/<subject>`, `kionga:group/<id>`, `kionga:policy/<id>` | `kionga:user/alice` |
| `kionga:blog/<id>`, `kionga:connection/<id>` | `kionga:connection/conn-1` |

Pipelines are named by their definition ID; ad hoc runs without a definition
use the run name. Creating a new definition is checked against
`kionga:project/<project>/pipeline/*`, and creating a project against
`kionga:project/*`.

Pattern rules:

- `*` alone matches any resource.
- A `*` segment matches exactly one segment: `kionga:project/*/pipeline/train`.
- A final `*` segment matches one or more remaining segments:
  `kionga:project/prj-a/*` covers every pipeline and workspace in `prj-a`, but
  not the project itself. Use both `kionga:project/prj-a` and
  `kionga:project/prj-a/*` to cover a project and its contents.

## Conditions

A statement applies only when every condition it sets holds. Inside one
condition, listed values are alternatives. A deny whose condition does not
hold does not apply.

| Condition | Meaning |
| --- | --- |
| `ip_cidr` | The source address is inside one of these networks. The source is the TCP peer; `X-Forwarded-For` is used only with `KIONGA_TRUSTED_PROXY=true`. |
| `utc_hours` | `{"start": 9, "end": 17}` UTC, end exclusive; `start > end` wraps midnight. |
| `resource_tags` | Project tags must equal these values. Tags come from project metadata: `template`, `framework`, `accelerator`, `profile`, `owner`. |
| `resource_profile` | `request.resource_profile` must be one of these profiles. It is the requested profile when creating a project and the caller's provisioned profile otherwise. |

Conditions with unknown inputs (no source address, no time) fail closed.

## Examples

Let an analysts group read one project and its pipelines:

```json
{
  "name": "Read churn project",
  "statements": [{
    "sid": "ReadChurn",
    "effect": "allow",
    "actions": ["project:Read", "pipeline:Read", "logs:Read"],
    "resources": ["kionga:project/prj-churn", "kionga:project/prj-churn/*"]
  }]
}
```

Freeze production runs outside office hours, for everyone the policy is
attached to, including administrators:

```json
{
  "name": "Production change window",
  "statements": [{
    "sid": "NoRunsAfterHours",
    "effect": "deny",
    "actions": ["pipeline:Run"],
    "resources": ["kionga:project/prj-prod/*"],
    "conditions": {"utc_hours": {"start": 18, "end": 7}}
  }]
}
```

Forbid one-off image overrides everywhere, and let an agent team run pipelines
in any project created from the production agent template:

```json
{
  "name": "Agent team pipelines",
  "statements": [
    {"sid": "NoImageOverrides", "effect": "deny", "actions": ["pipeline:OverrideImage"], "resources": ["*"]},
    {"sid": "RunAgentProjects", "effect": "allow", "actions": ["project:Read", "pipeline:Read", "pipeline:Run"],
     "resources": ["kionga:project/*"], "conditions": {"resource_tags": {"template": "production-agent"}}}
  ]
}
```

## Managing access in the console

**Users & access** has five tabs:

- **Users**: provision identities, roles, services, projects, storage and
  compute, and review access requests.
- **Groups**: create groups and manage members.
- **Policies**: built-in baselines (view only) and custom policies. The editor
  has a form mode and a JSON mode; validation errors point at the exact field,
  such as `statements[1].actions[0]`. **History** shows every immutable
  revision.
- **Attachments**: attach a policy to a user or group, or detach it.
- **Access simulator**: choose a principal, action and resource (and optionally
  a source IP and resource profile) to see allow or deny, the statement that
  decided, every matched statement, and the principal's effective policies.

When a request is denied by policy, the error toast offers **Why?**, which
re-evaluates the request for you and shows the trace.

## API

| Method and path | Purpose |
| --- | --- |
| `GET /api/v1/iam/catalog` | Actions, resource formats, condition keys, profiles |
| `GET /api/v1/iam/explain?principal=&action=&resource=&ip=&at=&resource_profile=` | Decision and trace. Anyone may explain themselves; explaining another principal needs `policy:Read` on `kionga:user/<subject>` |
| `GET /api/v1/iam/effective?principal=` | Every applicable policy and, per action, allowed and denied resource patterns |
| `GET/POST /api/v1/admin/iam/groups`, `GET/PUT/DELETE /api/v1/admin/iam/groups/{id}` | Groups |
| `GET/POST /api/v1/admin/iam/policies`, `GET/PUT/DELETE /api/v1/admin/iam/policies/{id}` | Policies; `PUT` bumps the version and accepts `version` for optimistic concurrency (`409` when stale) |
| `GET /api/v1/admin/iam/policies/{id}/revisions[/{version}]` | Immutable revisions |
| `GET/POST /api/v1/admin/iam/attachments`, `DELETE /api/v1/admin/iam/attachments/{id}` | Attachments (`{policy_id, principal_type: user|group, principal_id}`) |

Validation failures return `422` with `details: [{field, message}]`. Denials
return `403` with a `decision` object:

```json
{
  "error": "access_denied",
  "message": "Explicit deny: statement NoRunsAfterHours in policy \"Production change window\" (group:ml-team) denies pipeline:Run on kionga:project/prj-prod/pipeline/train. An explicit deny overrides every allow.",
  "decision": {
    "allowed": false,
    "action": "pipeline:Run",
    "resource": "kionga:project/prj-prod/pipeline/train",
    "decided_by": "production-change-window@v1#NoRunsAfterHours",
    "matched_statements": [...],
    "evaluated_policies": [...]
  }
}
```

`decided_by` is `<policy>@v<version>#<statement>`, `default-deny`,
`coarse-gate` (role or service assignment), or `account-suspended`. Reads of
resources you cannot read return `404`, so their existence is not disclosed.

## Safety rules

- **Anti-escalation.** Attaching a policy requires `policy:Attach` on it *and*
  that you already hold every action it allows on every resource it names
  (wildcards are expanded through the catalog; conditions are ignored, so the
  check is conservative). The same rule applies to editing an attached policy
  and to adding members to a group that has attachments. Refusals return `403`
  `privilege_escalation` listing each grant you lack.
- **Self-lockout.** You cannot attach to yourself (directly or through a group)
  a deny that removes your own `policy:Attach` on that policy.
- **Last administrator.** Demoting, suspending or deleting the last enabled
  administrator returns `409 last_admin`. The acting administrator and, in
  local sign-in mode, the bootstrap administrator count.
- **Built-ins are immutable.** IDs starting with `kionga-` are reserved.
- **Deletion order.** Detach a policy before deleting it; detach a group's
  policies before deleting the group. Revisions are kept after deletion.
- **Audit.** Every group, policy, revision and attachment change writes an
  audit event (`iam.*`) in the same transaction.
- **Live streams.** The `/api/v1/events` digest only counts runs, agents,
  sessions, models and features the caller can read, and is re-evaluated on
  every tick, so policy changes apply to open streams.

## Troubleshooting

- *"Your role and service assignments do not open ..."*: the coarse gate
  refused. Assign the service or role; policies cannot open an API surface the
  role and services keep closed.
- *Default deny*: no statement allowed the action. Use the simulator to check
  the resource name; remember a final `*` does not match the parent.
- *A grant does not apply*: check the statement's conditions with the
  simulator's source IP and resource profile fields.
- *Explaining another user shows fewer roles than expected*: explain uses the
  user's access profile. Roles that come only from OIDC claims are visible
  when the user explains themselves.

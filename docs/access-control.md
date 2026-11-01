# Access rules and policy evaluation

Access combines identity, role baselines, user and group policy attachments,
service/project assignments and operational resource limits. The gateway is the
authority; hiding a UI action is not an authorization boundary.

## Define access

1. Provision the user's stable subject, role, services, projects and compute/storage limits.
2. Create a group containing user subjects. IDs use 2–63 lowercase letters, digits or hyphens. Groups do not nest.
3. Create a policy containing named statements with `effect`, `actions`, `resources` and optional `conditions`.
4. Attach the policy to a user or group. A policy without an attachment does not grant anything.
5. Simulate the actual subject/action/resource request before applying broad changes. Inspect evaluated policies, matched statements and the decision reason.

Resources use `kionga:project/<project-id>/pipeline/<definition-id>` for pipelines.
Use `*` only where a scoped pattern is intended. Actions are namespaced, for
example `pipeline:Read` or `pipeline:Run`. The Access simulator lists supported
actions and resource examples. Managed role baselines are read-only.

## How rules combine

The gateway collects all applicable role baselines, direct user attachments and
attachments to the user's groups. These are evaluated together. There is no
ordering by attachment time, group hierarchy or specificity.

| Condition | Result |
| --- | --- |
| Account suspended | Denied before evaluating statements |
| Any applicable, matching explicit deny | Denied, even when an administrator baseline or a more specific statement allows it |
| No matching deny and at least one matching allow | Policy evaluation allows the request |
| No matching allow | Default deny |

A statement matches both its action and resource. All configured condition types
are combined with AND; alternatives within one condition use OR. Supported
conditions include source IP CIDRs, a half-open UTC hour range, resource tags and
resource profile. All configured resource-tag pairs must match. A condition that
does not hold skips the entire statement, including a deny statement.

Policy authorization does not bypass independent service grants, assigned project
boundaries, compute/storage quotas, pipeline override limits or workspace
isolation. The applicable API also checks those constraints.

## Changes and propagation

An active policy update affects all its attached users and current group members.
Removing a user from a group or detaching a policy removes that source of access;
other group memberships, direct attachments or role baselines may still allow the
request. Use an explicit deny when the intent is to prohibit an action regardless
of another allow. Deleting a grant alone does not implement a deny.

Policies retain revisions and changes are audited. Group membership and policy
attachments do not create nested inheritance. The last administrator cannot be
removed through access management. Simulating another user's identity uses its
provisioned profile; identity-provider-only roles may be absent from that preview,
so compare the actual user's effective-policy view when investigating a decision.

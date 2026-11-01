import { CodeBlock } from "@/components/ui/CodeBlock";
const example = {
  name: "Run project training",
  statements: [
    {
      sid: "RunTraining",
      effect: "allow",
      actions: ["pipeline:Read", "pipeline:Run", "logs:Read"],
      resources: ["kionga:project/prj-training/pipeline/*"],
    },
    {
      sid: "ProtectProduction",
      effect: "deny",
      actions: ["pipeline:Run"],
      resources: ["kionga:project/prj-production/pipeline/*"],
    },
  ],
};
export function AccessGuide() {
  return (
    <details className="panel access-guide">
      <summary>How roles, groups, policies and resource grants apply</summary>
      <div className="stack">
        <ol>
          <li>
            <strong>Start with the identity and resource grant.</strong> A user
            has role baselines, service access, project assignments and resource
            limits. Suspended accounts are denied before policy evaluation.
          </li>
          <li>
            <strong>Collect applicable policies.</strong> Evaluate role
            baselines together with policies attached directly to the user and
            to every group containing that user's subject. Groups contain users,
            not nested groups; there is no parent/child policy cascade.
          </li>
          <li>
            <strong>Match the request.</strong> A statement must match both its
            action and resource. All configured condition types must hold;
            choices within a condition are alternatives. An unmet condition
            skips that statement.
          </li>
          <li>
            <strong>Resolve the decision.</strong> Any matching explicit deny
            wins, including against administrator or directly attached allows.
            Otherwise any matching allow grants access. No matching allow means
            default deny. A more specific allow never overrides a broader deny.
          </li>
          <li>
            <strong>Apply operational limits.</strong> Project ownership,
            service grants, compute/storage quotas, flow override rules and
            workspace isolation still apply. A policy allow does not bypass
            these checks.
          </li>
        </ol>
        <h3>Defining and changing access</h3>
        <p>
          Create a group with stable user subjects, create a policy with scoped
          action/resource statements, then attach that policy to the group.
          Updating its active version applies to every current member. Removing
          membership or an attachment removes that grant; another role, group or
          direct policy may still allow the action.
        </p>
        <p>
          Group IDs use 2 to 63 lowercase letters, digits or hyphens. Use explicit
          project IDs in resource patterns and select only the actions required.
          Managed role policies are read-only. Removing all access from the last
          administrator is blocked.
        </p>
        <CodeBlock language="json" filename="example-access-policy.json">
          {JSON.stringify(example, null, 2)}
        </CodeBlock>
        <p>
          Replace the example project IDs with actual IDs. Use the{" "}
          <a href="/console/access?tab=simulator">policy simulator</a> with the
          actual subject, action, resource, IP and time before applying a
          change. Review matched statements and policy sources in the decision
          trace.
        </p>
      </div>
    </details>
  );
}

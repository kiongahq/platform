// The roles a person can be given when an account is provisioned or edited. This
// list must equal store.UserRoles in go/internal/store/access.go, in the same
// order; a Go test reads this file and fails when the two differ. The "service"
// role is the machine identity of in-platform services and is never offered here.
export const USER_ROLES = [
  { value: 'user', label: 'user' },
  { value: 'viewer', label: 'viewer' },
  { value: 'engineer', label: 'engineer' },
  { value: 'operator', label: 'operator' },
  { value: 'admin', label: 'admin' },
] as const;

export type UserRole = (typeof USER_ROLES)[number]['value'];

export const DEFAULT_USER_ROLE: UserRole = 'user';

// One line per role, taken from the role baselines in go/internal/policy/baseline.go.
export const USER_ROLE_HELP =
  'user: only the assigned services, on assigned and owned projects. ' +
  'viewer: read-only access to projects, pipelines, logs and features. ' +
  'engineer: read everything in scope, build projects, pipelines and features, open workspaces. ' +
  'operator: full platform operation, including user and policy administration. ' +
  'admin: full platform administration.';

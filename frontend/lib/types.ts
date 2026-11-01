export type Entity = Record<string, any>;
export type Identity = {
  subject: string;
  email?: string;
  roles: string[];
  services: string[];
  mode: string;
  permissions: Record<string, boolean>;
  project_ids?: string[];
  provisioned?: boolean;
  entitlements?: Entity | null;
  storage?: Entity;
  compute?: Entity;
  [key: string]: any;
};
export const isAdministrator = (identity: Identity) =>
  identity.roles.some((role) => ["admin", "operator"].includes(role));
export const hasService = (identity: Identity, service: string) =>
  isAdministrator(identity) ||
  (identity.roles.length > 0 && !identity.roles.includes("user")) ||
  identity.services.includes(service);
export const canWrite = (identity: Identity, permission: string) =>
  identity.permissions[permission] === true;
export function items<T = Entity>(value: any): T[] {
  return Array.isArray(value)
    ? value
    : Array.isArray(value?.items)
      ? value.items
      : [];
}

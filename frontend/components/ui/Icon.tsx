import type { SVGProps } from "react";
/** Licensed Material Design Icons, self-hosted with the application. */
export function Icon({
  name,
  ...props
}: SVGProps<SVGSVGElement> & { name: string }) {
  const alias: Record<string, string> = {
    "agent-designer": "agents",
    langflow: "agents",
    workbench: "workspace",
    ide: "edit",
  };
  return (
    <svg
      {...props}
      className={`material-icon ${props.className || ""}`}
      viewBox="0 0 24 24"
      aria-hidden="true"
      focusable="false"
    >
      <use href={`/icons.svg#${alias[name] || name}`} />
    </svg>
  );
}

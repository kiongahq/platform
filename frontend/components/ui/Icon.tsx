import type { SVGProps } from "react";
import SaveAsIcon from "@mui/icons-material/SaveAs";
/** Shared Material icons, with SaveAs identifying object storage. */
export function Icon({
  name,
  ...props
}: SVGProps<SVGSVGElement> & { name: string }) {
  if (name === "storage" || name === "bucket") {
    const { color, shapeRendering, ...svgProps } = props;
    return (
      <SaveAsIcon
        {...svgProps}
        htmlColor={color}
        shapeRendering={shapeRendering === undefined ? undefined : String(shapeRendering)}
        fontSize="inherit"
        className={`material-icon ${props.className || ""}`}
        style={{ width: 20, height: 20, ...props.style }}
        aria-hidden="true"
        focusable="false"
        data-storage-icon="save-as"
      />
    );
  }
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

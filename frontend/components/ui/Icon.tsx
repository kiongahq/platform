import type { SVGProps } from "react";
import AccountTreeOutlinedIcon from "@mui/icons-material/AccountTreeOutlined";
import HubOutlinedIcon from "@mui/icons-material/HubOutlined";
import SaveAsIcon from "@mui/icons-material/SaveAs";
const materialIcons = {
  "agent-designer": AccountTreeOutlinedIcon,
  langflow: HubOutlinedIcon,
  storage: SaveAsIcon,
  bucket: SaveAsIcon,
};
/** Shared Material icons for navigation and resource actions. */
export function Icon({
  name,
  ...props
}: SVGProps<SVGSVGElement> & { name: string }) {
  const MaterialIcon = materialIcons[name as keyof typeof materialIcons];
  if (MaterialIcon) {
    const { color, shapeRendering, ...svgProps } = props;
    return (
      <MaterialIcon
        {...svgProps}
        htmlColor={color}
        shapeRendering={shapeRendering === undefined ? undefined : String(shapeRendering)}
        fontSize="inherit"
        className={`material-icon ${props.className || ""}`}
        style={{ width: 20, height: 20, ...props.style }}
        aria-hidden="true"
        focusable="false"
        data-storage-icon={name === "storage" || name === "bucket" ? "save-as" : undefined}
      />
    );
  }
  const alias: Record<string, string> = {
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

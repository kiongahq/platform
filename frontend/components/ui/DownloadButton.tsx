"use client";
import type { ReactNode } from "react";
import { Icon } from "./Icon";
export function DownloadButton({
  filename,
  content,
  language = "text",
  children,
}: {
  filename: string;
  content: string;
  language?: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      disabled={!content}
      onClick={() => {
        const url = URL.createObjectURL(
          new Blob([content], {
            type:
              language === "yaml"
                ? "application/yaml;charset=utf-8"
                : "text/plain;charset=utf-8",
          }),
        );
        const link = document.createElement("a");
        link.href = url;
        link.download = filename;
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
      }}
    >
      <Icon name="download" />
      {children}
    </button>
  );
}

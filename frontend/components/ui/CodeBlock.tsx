"use client";
import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import {
  formatCode,
  detectLanguage,
  type CodeLanguage,
} from "@/lib/code-format";
import { Icon } from "./Icon";

function tokens(line: string, language: string): ReactNode {
  if (language === "text" || language === "plaintext") return line;
  const pattern =
    /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|#[^\n]*|\/\/[^\n]*|\b(?:true|false|null|None|True|False|import|from|def|return|class|const|let|async|await|export|if|else|for|in|SELECT|FROM|WHERE)\b|\b\d+(?:\.\d+)?\b)/g;
  return line.split(pattern).map((part, index) =>
    !part ? (
      part
    ) : (
      <span
        key={index}
        className={
          /^(#|\/\/)/.test(part)
            ? "syntax-comment"
            : /^["']/.test(part)
              ? "syntax-string"
              : /^(true|false|null|None|True|False|import|from|def|return|class|const|let|async|await|export|if|else|for|in|SELECT|FROM|WHERE)$/.test(
                    part,
                  )
                ? "syntax-keyword"
                : /^\d/.test(part)
                  ? "syntax-number"
                  : undefined
        }
      >
        {part}
      </span>
    ),
  );
}
export function CodeBlock({
  children,
  language,
  filename,
  className = "",
  id,
  format = true,
  copy = true,
  lineClasses = [],
}: {
  children: string;
  language?: CodeLanguage;
  filename?: string;
  className?: string;
  id?: string;
  format?: boolean;
  copy?: boolean;
  lineClasses?: string[];
}) {
  const generatedId = useId(),
    [wrapped, setWrapped] = useState(false),
    [copied, setCopied] = useState(false),
    [error, setError] = useState("");
  const raw = String(children ?? "");
  const lang = language || detectLanguage(raw);
  const source = useMemo(() => {
    if (!format) return raw;
    try {
      return formatCode(raw, lang);
    } catch {
      return raw;
    }
  }, [raw, lang, format]);
  useEffect(()=>setCopied(false),[source]);
  const lines = source.replace(/\n$/, "").split("\n");
  return (
    <div className={`code-frame ${className}`} data-language={lang}>
      <div className="code-frame-header">
        <span>
          {filename || (lang === "text" ? "Output" : lang.toUpperCase())}
        </span>
        <div className="code-tools">
          <button
            type="button"
            aria-label="Toggle line wrapping"
            aria-pressed={wrapped}
            title="Wrap lines"
            onClick={() => setWrapped(!wrapped)}
          >
            <Icon name="wrap" />
          </button>
          {copy && (
            <button
              type="button"
              aria-label={copied ? "Code copied" : "Copy code"}
              title="Copy code"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(source);
                  setCopied(true);
                  setError("");
                } catch {
                  setError("Copy unavailable. Select the code to copy it.");
                }
              }}
            >
              <Icon name={copied ? "check" : "copy"} />
            </button>
          )}
        </div>
      </div>
      <pre
        id={id || generatedId}
        className={`numbered-code${wrapped ? " code-wrapped" : ""}`}
        tabIndex={0}
        role="region"
        aria-label={filename || "Source code"}
      >
        <code>
          {lines.map((line, index) => (
            <span
              className={`code-line ${lineClasses[index] || ""}`}
              key={index}
            >
              <span className="code-line-number" aria-hidden="true">
                {index + 1}
              </span>
              <span className="code-line-text">
                {tokens(line, lang)}
                {"\n"}
              </span>
            </span>
          ))}
        </code>
      </pre>
      {error && (
        <p className="code-copy-error" role="status">
          {error}
        </p>
      )}
    </div>
  );
}

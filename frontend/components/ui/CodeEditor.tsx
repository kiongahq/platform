"use client";
import {
  forwardRef,
  useId,
  useRef,
  useState,
  type TextareaHTMLAttributes,
  type ForwardedRef,
} from "react";
import { formatCode, type CodeLanguage } from "@/lib/code-format";
import { Icon } from "./Icon";
type Props = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  language?: CodeLanguage;
  label?: string;
  filename?: string;
  onValueChange?: (value: string) => void;
};
export const CodeEditor = forwardRef(function CodeEditor(
  {
    language = "json",
    label,
    filename,
    onValueChange,
    onChange,
    onScroll,
    className = "",
    value,
    defaultValue,
    ...props
  }: Props,
  forwarded: ForwardedRef<HTMLTextAreaElement>,
) {
  const autoId = useId(),
    id = props.id || autoId,
    ref = useRef<HTMLTextAreaElement>(null),
    gutter = useRef<HTMLDivElement>(null);
  const [text, setText] = useState(String(value ?? defaultValue ?? "")),
    [error, setError] = useState("");
  const content = value === undefined ? text : String(value),
    count = Math.max(1, content.split("\n").length);
  function assign(node: HTMLTextAreaElement | null) {
    ref.current = node;
    if (typeof forwarded === "function") forwarded(node);
    else if (forwarded) forwarded.current = node;
  }
  function format() {
    const node = ref.current;
    if (!node || props.readOnly || props.disabled) return;
    try {
      const top = node.scrollTop, left = node.scrollLeft, start = node.selectionStart, end = node.selectionEnd;
      const next = formatCode(node.value, language);
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")!.set!;
      setter.call(node, next);
      node.dispatchEvent(new window.Event("input", {bubbles:true}));
      setText(next);
      setError("");
      node.focus({preventScroll:true});
      node.setSelectionRange(Math.min(start,next.length),Math.min(end,next.length));
      node.scrollTop=top;node.scrollLeft=left;
      if(gutter.current)gutter.current.style.transform=`translateY(${-top}px)`;
    } catch (failure) {
      setError((failure as Error).message);
      node.focus();
    }
  }
  return (
    <div className={`code-editor ${className}`} data-language={language}>
      <div className="code-editor-header">
        <label htmlFor={id}>{filename || language.toUpperCase()}</label>
        {["json", "yaml", "yml"].includes(language) &&
          !props.readOnly &&
          !props.disabled && (
            <button
              type="button"
              onClick={format}
              aria-label={`Format ${label || language.toUpperCase()}`}
            >
              <Icon name="format" />
              Format
            </button>
          )}
      </div>
      <div className="code-editor-body">
        <div className="code-editor-gutter" aria-hidden="true">
          <div ref={gutter}>
            {Array.from({ length: count }, (_, index) => (
              <span key={index}>{index + 1}</span>
            ))}
          </div>
        </div>
        <textarea
          {...props}
          id={id}
          ref={assign}
          value={value}
          defaultValue={defaultValue}
          aria-label={label || props["aria-label"]}
          className="numbered-textarea"
          wrap="off"
          spellCheck={false}
          onKeyDown={(event) => {
            if (
              event.altKey &&
              event.shiftKey &&
              event.key.toLowerCase() === "f"
            ) {
              event.preventDefault();
              format();
            }
            props.onKeyDown?.(event);
          }}
          onScroll={(event) => {
            if (gutter.current)
              gutter.current.style.transform = `translateY(${-event.currentTarget.scrollTop}px)`;
            onScroll?.(event);
          }}
          onChange={(event) => {
            setText(event.target.value);
            setError("");
            onValueChange?.(event.target.value);
            onChange?.(event);
          }}
        />
      </div>
      {error && (
        <p className="code-format-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
});

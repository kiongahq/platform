"use client";
import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type SelectHTMLAttributes,
} from "react";
import { Icon } from "./Icon";

type Choice = {
  value: string;
  label: string;
  disabled: boolean;
  group?: string;
};

function choicesOf(select: HTMLSelectElement): Choice[] {
  return Array.from(select.options).map((option) => ({
    value: option.value,
    label: option.label || option.text,
    disabled: option.disabled,
    group:
      option.parentElement?.tagName === "OPTGROUP"
        ? (option.parentElement as HTMLOptGroupElement).label
        : undefined,
  }));
}

/** Tell React about a chosen value the same way a person using the control would. */
function commit(select: HTMLSelectElement, value: string) {
  if (select.value === value) return;
  Object.getOwnPropertyDescriptor(Object.getPrototypeOf(select), "value")?.set?.call(
    select,
    value,
  );
  const view = select.ownerDocument.defaultView as Window & typeof globalThis;
  select.dispatchEvent(new view.Event("change", { bubbles: true }));
}

const nextEnabled = (choices: Choice[], from: number, step: 1 | -1) => {
  for (let i = from + step; i >= 0 && i < choices.length; i += step)
    if (!choices[i].disabled) return i;
  return from;
};

/**
 * A native select stays in the page for labels, forms, focus and assistive technology.
 * Browsers draw their own list for it, and only some can be styled, so a single select
 * opens the application's list instead. Multiple selects are inline and stay native.
 */
export const Select = forwardRef<
  HTMLSelectElement,
  SelectHTMLAttributes<HTMLSelectElement>
>(function Select({ onMouseDown, onKeyDown, onBlur, ...props }, ref) {
  const select = useRef<HTMLSelectElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const id = useId();
  const [choices, setChoices] = useState<Choice[] | null>(null);
  const [active, setActive] = useState(0);
  const [place, setPlace] = useState({ left: 0, top: 0, width: 0, maxHeight: 320 });
  useImperativeHandle(ref, () => select.current as HTMLSelectElement);
  const open = choices !== null;

  const close = useCallback(() => setChoices(null), []);
  const show = () => {
    const element = select.current;
    if (!element || element.disabled) return;
    const found = choicesOf(element);
    if (!found.length) return;
    const box = element.getBoundingClientRect();
    const below = window.innerHeight - box.bottom - 12;
    const above = box.top - 12;
    const flip = below < 200 && above > below;
    setPlace({
      left: box.left,
      width: box.width,
      top: flip ? Math.max(8, box.top - Math.min(above, 320) - 4) : box.bottom + 4,
      maxHeight: Math.min(flip ? above : below, 320),
    });
    setActive(Math.max(0, element.selectedIndex));
    setChoices(found);
  };
  const choose = (index: number) => {
    const choice = choices?.[index];
    if (!choice || choice.disabled || !select.current) return;
    commit(select.current, choice.value);
    close();
  };

  useEffect(() => {
    if (!open) return;
    const element = list.current as (HTMLUListElement & { showPopover?: () => void }) | null;
    // The top layer keeps the list above dialogs and outside clipped containers.
    try {
      element?.showPopover?.();
    } catch {
      /* Already shown, or unsupported: fixed positioning still applies. */
    }
    const outside = (event: Event) => {
      const target = event.target as Node;
      if (!list.current?.contains(target) && target !== select.current) close();
    };
    const moved = (event: Event) => {
      if (!list.current?.contains(event.target as Node)) close();
    };
    document.addEventListener("pointerdown", outside, true);
    window.addEventListener("scroll", moved, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      window.removeEventListener("scroll", moved, true);
      window.removeEventListener("resize", close);
    };
  }, [open, close]);

  useEffect(() => {
    if (open)
      list.current
        ?.querySelector('[data-active="true"]')
        ?.scrollIntoView?.({ block: "nearest" });
  }, [open, active]);

  const mouseDown = (event: MouseEvent<HTMLSelectElement>) => {
    onMouseDown?.(event);
    if (props.multiple || event.defaultPrevented) return;
    event.preventDefault();
    select.current?.focus();
    if (open) close();
    else show();
  };
  const keyDown = (event: KeyboardEvent<HTMLSelectElement>) => {
    onKeyDown?.(event);
    if (props.multiple || event.defaultPrevented) return;
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
        event.preventDefault();
        show();
      }
      return;
    }
    const items = choices as Choice[];
    const moves: Record<string, number> = {
      ArrowDown: nextEnabled(items, active, 1),
      ArrowUp: nextEnabled(items, active, -1),
      Home: nextEnabled(items, -1, 1),
      End: nextEnabled(items, items.length, -1),
    };
    if (event.key in moves) {
      event.preventDefault();
      if (moves[event.key] >= 0 && moves[event.key] < items.length)
        setActive(moves[event.key]);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      choose(active);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close();
    } else if (event.key === "Tab") {
      close();
    } else if (event.key.length === 1) {
      const start = (active + 1) % items.length;
      const typed = event.key.toLowerCase();
      for (let i = 0; i < items.length; i++) {
        const index = (start + i) % items.length;
        if (!items[index].disabled && items[index].label.toLowerCase().startsWith(typed)) {
          setActive(index);
          break;
        }
      }
    }
  };

  return (
    <span
      className={`select-control${props.multiple ? " select-multiple" : ""}`}
    >
      <select
        {...props}
        ref={select}
        onMouseDown={mouseDown}
        onKeyDown={keyDown}
        onBlur={(event) => {
          onBlur?.(event);
          close();
        }}
        aria-expanded={props.multiple ? undefined : open}
        aria-controls={open ? `${id}-list` : undefined}
        aria-activedescendant={open ? `${id}-${active}` : undefined}
      />
      {!props.multiple && <Icon name="chevron-down" />}
      {open && (
        <ul
          ref={list}
          id={`${id}-list`}
          role="listbox"
          popover="manual"
          className="select-list"
          style={place}
          onMouseDown={(event) => event.preventDefault()}
        >
          {(choices as Choice[]).map((choice, index) => (
            <li
              key={index}
              id={`${id}-${index}`}
              role="option"
              aria-selected={select.current?.selectedIndex === index}
              aria-disabled={choice.disabled || undefined}
              data-active={index === active}
              data-group={
                choice.group && choice.group !== choices?.[index - 1]?.group
                  ? choice.group
                  : undefined
              }
              onMouseEnter={() => !choice.disabled && setActive(index)}
              onClick={() => choose(index)}
            >
              {choice.label || "\u00a0"}
            </li>
          ))}
        </ul>
      )}
    </span>
  );
});

"use client";
import { forwardRef, type SelectHTMLAttributes } from "react";
import { Icon } from "./Icon";
/** Keep native form, focus and keyboard behavior with consistent field chrome. */
export const Select = forwardRef<
  HTMLSelectElement,
  SelectHTMLAttributes<HTMLSelectElement>
>(function Select(props, ref) {
  return (
    <span
      className={`select-control${props.multiple ? " select-multiple" : ""}`}
    >
      <select {...props} ref={ref} />
      {!props.multiple && <Icon name="chevron-down" />}
    </span>
  );
});

import type { ReactNode } from "react";
import { Check, ChevronDown } from "lucide-react";
import { usePopover } from "./Toolbar";

export interface Option {
  value: string;
  label: string;
}

interface Props {
  value: string;
  options: Option[];
  onChange: (value: string) => void;
  /** Names the control for screen readers. */
  label: string;
  icon?: ReactNode;
  /** Caps the button width; long labels truncate. */
  maxWidth?: number;
  /** Which edge the menu lines up with: right in the toolbar, left in filter rows. */
  align?: "left" | "right";
}

/** A dropdown styled like the toolbar's other menus (the time range picker). */
export function Select({ value, options, onChange, label, icon, maxWidth, align = "right" }: Props) {
  const { open, setOpen, ref } = usePopover();
  const current = options.find((o) => o.value === value) ?? options[0];
  return (
    <div className="popover-anchor" ref={ref}>
      <button type="button" className="btn" onClick={() => setOpen(!open)} aria-haspopup="menu" aria-expanded={open} aria-label={`${label}: ${current?.label ?? ""}`} style={{ maxWidth }}>
        {icon}
        <span className="btn-label">{current?.label}</span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div className="popover menu-scroll" role="menu" style={align === "left" ? { minWidth: 200, left: 0, right: "auto" } : { minWidth: 200 }}>
          {options.map((o) => {
            const selected = o.value === value;
            return (
              <button type="button"
                key={o.value}
                role="menuitemradio"
                aria-checked={selected}
                className={`menu-row ${selected ? "selected" : ""}`}
                onClick={() => {
                  onChange(o.value);
                  setOpen(false);
                }}
              >
                <span className="check">{selected && <Check size={16} strokeWidth={3} />}</span>
                <span className="menu-label">{o.label}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

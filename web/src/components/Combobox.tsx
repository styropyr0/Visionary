import { useMemo, useState, type KeyboardEvent } from "react";
import { Check } from "lucide-react";
import { FloatingMenu } from "./Floating";
import { usePopover } from "./Toolbar";

interface Props {
  value: string;
  onChange: (value: string) => void;
  /** Suggestions; any other text can still be typed. */
  options: string[];
  placeholder?: string;
  /** Names the control for screen readers. */
  label: string;
}

const MAX_SHOWN = 50;

/**
 * A text field with suggestions, in the same menu style as Select. Use it where
 * the value is free text but usually one of a known list (routes, services).
 */
export function Combobox({ value, onChange, options, placeholder, label }: Props) {
  const { open, setOpen, ref, menuRef } = usePopover();
  const [active, setActive] = useState(-1);

  const matches = useMemo(() => {
    const q = value.trim().toLowerCase();
    const list = q ? options.filter((o) => o.toLowerCase().includes(q)) : options;
    return list.slice(0, MAX_SHOWN);
  }, [options, value]);

  const pick = (v: string) => {
    onChange(v);
    setOpen(false);
    setActive(-1);
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setOpen(true);
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => (matches.length ? (i + step + matches.length) % matches.length : -1));
    } else if (e.key === "Enter" && open && active >= 0 && matches[active]) {
      e.preventDefault();
      pick(matches[active]);
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  };

  return (
    <div className="popover-anchor combobox" ref={ref}>
      <input
        className="input"
        value={value}
        placeholder={placeholder}
        aria-label={label}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        onFocus={() => setOpen(true)}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
          setActive(-1);
        }}
        onKeyDown={onKey}
      />
      {open && matches.length > 0 && (
        <FloatingMenu anchor={ref} menuRef={menuRef} matchWidth className="menu-scroll combobox-menu" role="listbox">
          {matches.map((o, i) => (
            <button
              key={o}
              type="button"
              role="option"
              aria-selected={o === value}
              className={`menu-row ${o === value ? "selected" : ""} ${i === active ? "active" : ""}`}
              // Keep focus in the input so typing continues after a hover.
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => pick(o)}
            >
              <span className="check">{o === value && <Check size={16} strokeWidth={3} />}</span>
              <span className="menu-label">{o}</span>
            </button>
          ))}
        </FloatingMenu>
      )}
    </div>
  );
}

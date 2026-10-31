import { useState } from "react";
import { Tag } from "lucide-react";
import { Select } from "../components/Select";
import type { UptimeCheck } from "../lib/uptime";

/** Picks one tag to narrow the check list to; "" shows every check. */
export function TagFilter({ checks, value, onChange }: { checks: UptimeCheck[]; value: string; onChange: (tag: string) => void }) {
  const tags = [...new Set(checks.flatMap((c) => c.tags ?? []))].sort();
  if (tags.length === 0) return null;
  const options = [{ value: "", label: "All tags" }, ...tags.map((t) => ({ value: t, label: t }))];
  return <Select label="Tag" icon={<Tag size={14} />} value={tags.includes(value) ? value : ""} options={options} onChange={onChange} />;
}

export function TagPills({ tags }: { tags?: string[] }) {
  if (!tags?.length) return null;
  return (
    <span className="up-tags">
      {tags.map((t) => (
        <span className="pill" key={t}>
          {t}
        </span>
      ))}
    </span>
  );
}

/** Tags typed as one line, split on commas and spaces. */
export function TagsField({ value, onChange }: { value: string[]; onChange: (tags: string[]) => void }) {
  const [text, setText] = useState(value.join(", "));
  return (
    <label className="field">
      <span>Tags</span>
      <input
        className="input"
        value={text}
        placeholder="Optional, e.g. smoke, checkout"
        onChange={(e) => {
          setText(e.target.value);
          onChange(
            e.target.value
              .split(/[\s,]+/)
              .map((t) => t.trim().toLowerCase())
              .filter(Boolean),
          );
        }}
      />
      <small>Group checks to filter the list, and to run a group from CI, such as every check tagged smoke after a deploy.</small>
    </label>
  );
}

/** The tag in the hash (#/uptime?tag=smoke), so a filtered list can be linked. */
export function readTagParam(): string {
  const q = window.location.hash.split("?")[1] ?? "";
  return new URLSearchParams(q).get("tag") ?? "";
}

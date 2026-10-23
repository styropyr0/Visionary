import { useMemo, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { formatCount } from "../../lib/format";
import { countParams, formatSql, joins, sqlLinesText, type SqlLine } from "../../lib/sql";
import { CopyButton } from "../Modal";

/** Lines shown before "Show all". */
const COLLAPSED_LINES = 18;

/**
 * A stored statement, laid out and highlighted. The SDK keeps it on one line
 * with values replaced by ?, which this marks, so the statement reads like
 * the query the application wrote. "As stored" shows the text unchanged.
 */
export function SqlView({ sql, compact = false }: { sql: string; compact?: boolean }) {
  const [stored, setStored] = useState(false);
  const [all, setAll] = useState(false);
  const lines = useMemo(() => formatSql(sql), [sql]);
  const text = useMemo(() => sqlLinesText(lines), [lines]);
  const params = useMemo(() => countParams(sql), [sql]);
  const long = !stored && lines.length > COLLAPSED_LINES;
  const shown = long && !all ? lines.slice(0, COLLAPSED_LINES) : lines;
  const multiline = lines.length > 1;

  return (
    <div className={`sqlv ${compact ? "compact" : ""}`}>
      <div className="sqlv-bar">
        {multiline && (
          <div className="segmented" role="radiogroup" aria-label="Layout">
            <button type="button" role="radio" aria-checked={!stored} className={!stored ? "on" : ""} onClick={() => setStored(false)}>
              Formatted
            </button>
            <button type="button" role="radio" aria-checked={stored} className={stored ? "on" : ""} onClick={() => setStored(true)}>
              As stored
            </button>
          </div>
        )}
        <span className="sqlv-meta dim">
          {multiline && <>{formatCount(lines.length, false)} lines · </>}
          {formatCount(sql.length, false)} chars
          {params.removed > 0 && (
            <>
              {" · "}
              <span className="sqlv-param" title="Values the SDK removed before storing the statement">
                ?
              </span>{" "}
              {formatCount(params.removed)} removed {params.removed === 1 ? "value" : "values"}
            </>
          )}
          {params.app > 0 && <> · {formatCount(params.app)} app parameters</>}
        </span>
        <span className="sqlv-copy">
          <CopyButton text={stored ? sql : text} />
        </span>
      </div>

      {stored ? (
        <pre className="sqlv-code sqlv-stored mono">{sql}</pre>
      ) : (
        <div className={`sqlv-code mono ${long && !all ? "clipped" : ""}`} role="region" aria-label="Statement">
          {shown.map((l, n) => (
            <div key={n} className="sqlv-line">
              {multiline && <span className="sqlv-ln" aria-hidden>{n + 1}</span>}
              <span className="sqlv-text" style={{ paddingLeft: `${l.indent * 2}ch` }}>
                <Tokens line={l} />
              </span>
            </div>
          ))}
        </div>
      )}

      {long && (
        <button type="button" className="btn ghost sm sqlv-more" onClick={() => setAll((a) => !a)}>
          {all ? (
            <>
              <ChevronUp size={13} /> Show less
            </>
          ) : (
            <>
              <ChevronDown size={13} /> Show all {formatCount(lines.length, false)} lines
            </>
          )}
        </button>
      )}
    </div>
  );
}

function Tokens({ line }: { line: SqlLine }) {
  return (
    <>
      {line.tokens.map((t, i) => (
        <span key={i}>
          {i > 0 && joins(line.tokens[i - 1], t) ? " " : ""}
          <span className={`sq-${t.kind}`}>{t.text}</span>
        </span>
      ))}
    </>
  );
}

using System;
using System.Buffers;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary;

/// <summary>
/// The text rules every SDK applies to app_logs lines (SPEC.md "Application logs"): secret
/// scrubbing, the attrs encoding and its caps, and the level a plain-text line states.
/// </summary>
internal static class LogText
{
    public const int MaxMessage = 8 << 10;
    public const int MaxAttrs = 16 << 10;
    private const int MaxAttrValue = 1024;

    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(1);
    private static readonly JsonWriterOptions JsonOptions = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };

    /// <summary>Secrets in free text, replaced in this order before a line is stored.</summary>
    private static readonly (Regex Re, string With)[] SecretPatterns =
    {
        (Re(@"-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)"), "[REDACTED:private-key]"),
        (Re(@"\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}"), "[REDACTED:jwt]"),
        (Re(@"(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}"), "$1 [REDACTED]"),
        (Re(@"\b(AKIA|ASIA)[0-9A-Z]{16}\b"), "[REDACTED:aws-key]"),
        (Re(@"(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\b([""']?\s*[=:]\s*)(""[^""]*""|'[^']*'|[^\s,;&""']+)"), "$1$2[REDACTED]"),
    };

    private static readonly Regex LevelField = Re(@"(?i)\b(?:level|lvl|severity)=[""']?([a-z]+)");

    /// <summary>A date, a time or both, as loggers print them.</summary>
    private static readonly Regex TimestampWord = Re(@"^[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$");

    private static Regex Re(string pattern) => new(pattern, RegexOptions.Compiled | RegexOptions.CultureInvariant, Timeout);

    /// <summary>Replaces credentials in <paramref name="s"/>.</summary>
    public static string Scrub(string s)
    {
        foreach (var (re, with) in SecretPatterns)
        {
            try
            {
                if (re.IsMatch(s))
                {
                    s = re.Replace(s, with);
                }
            }
            catch (RegexMatchTimeoutException)
            {
                // a pathological line: store it unscrubbed rather than stall the writer
            }
        }
        return s;
    }

    /// <summary>The attribute names whose values are never stored: the request redaction defaults plus <paramref name="extra"/>.</summary>
    public static HashSet<string> SecretKeys(IEnumerable<string> extra) =>
        new(Redactor.DefaultFields.Concat(extra).Select(f => f.Trim()).Where(f => f.Length > 0), StringComparer.OrdinalIgnoreCase);

    /// <summary>
    /// Writes attrs as one flat JSON object in key order: values under a secret key (its last
    /// dotted part) redacted, strings scrubbed, at most <see cref="MaxAttrs"/> bytes. Long string
    /// values are cut first, then keys dropped (with "_truncated": true).
    /// </summary>
    public static string EncodeAttrs(IReadOnlyDictionary<string, object> attrs, ISet<string> secret)
    {
        var output = new SortedDictionary<string, object>(StringComparer.Ordinal);
        foreach (var (k, v) in attrs)
        {
            var last = k[(k.LastIndexOf('.') + 1)..];
            output[k] = secret.Contains(last) ? Redactor.Redacted : v is string s ? Scrub(s) : v;
        }
        var json = Json(output);
        if (Encoding.UTF8.GetByteCount(json) <= MaxAttrs)
        {
            return json;
        }
        foreach (var k in output.Keys.ToList())
        {
            if (output[k] is string s && Encoding.UTF8.GetByteCount(s) > MaxAttrValue)
            {
                output[k] = Truncate(s, MaxAttrValue) + "…";
            }
        }
        json = Json(output);
        if (Encoding.UTF8.GetByteCount(json) <= MaxAttrs)
        {
            return json;
        }
        var kept = new SortedDictionary<string, object>(StringComparer.Ordinal) { ["_truncated"] = true };
        foreach (var (k, v) in output)
        {
            kept[k] = v;
            if (Encoding.UTF8.GetByteCount(Json(kept)) > MaxAttrs)
            {
                kept.Remove(k);
                break;
            }
        }
        return Json(kept);
    }

    private static string Json(SortedDictionary<string, object> values)
    {
        var buf = new ArrayBufferWriter<byte>();
        using (var w = new Utf8JsonWriter(buf, JsonOptions))
        {
            w.WriteStartObject();
            foreach (var (k, v) in values)
            {
                w.WritePropertyName(k);
                switch (v)
                {
                    case bool b:
                        w.WriteBooleanValue(b);
                        break;
                    case long l:
                        w.WriteNumberValue(l);
                        break;
                    case ulong u:
                        w.WriteNumberValue(u);
                        break;
                    case double d when double.IsFinite(d):
                        w.WriteNumberValue(d);
                        break;
                    case double d:
                        w.WriteStringValue(d.ToString(CultureInfo.InvariantCulture));
                        break;
                    default:
                        w.WriteStringValue(v as string ?? Convert.ToString(v, CultureInfo.InvariantCulture));
                        break;
                }
            }
            w.WriteEndObject();
        }
        return Encoding.UTF8.GetString(buf.WrittenSpan);
    }

    /// <summary>Cuts <paramref name="s"/> to at most <paramref name="maxBytes"/> UTF-8 bytes on a character boundary.</summary>
    public static string Truncate(string s, int maxBytes)
    {
        if (s.Length * 3 <= maxBytes || Encoding.UTF8.GetByteCount(s) <= maxBytes)
        {
            return s;
        }
        int bytes = 0, i = 0;
        while (i < s.Length)
        {
            var c = s[i];
            int n, step = 1;
            if (c < 0x80)
            {
                n = 1;
            }
            else if (c < 0x800)
            {
                n = 2;
            }
            else if (char.IsHighSurrogate(c) && i + 1 < s.Length && char.IsLowSurrogate(s[i + 1]))
            {
                n = 4;
                step = 2;
            }
            else
            {
                n = 3;
            }
            if (bytes + n > maxBytes)
            {
                break;
            }
            bytes += n;
            i += step;
        }
        return s[..i];
    }

    /// <summary>
    /// The level a plain-text line states at its start, the way loggers print it: "ERROR ...",
    /// "[warn] ...", "error: ...", "2026-10-28 09:30:12 WARN ...", or a level=/lvl=/severity=
    /// field in the first 200 characters. A bare lower-case word ("error rate is low") is not a
    /// level. 0 when the line states none.
    /// </summary>
    public static int TextLevel(string s)
    {
        var head = s.Length > 200 ? s[..200] : s;
        var m = LevelField.Match(head);
        if (m.Success && Severity.Parse(m.Groups[1].Value) is > 0 and var fieldLevel)
        {
            return fieldLevel;
        }
        var words = head.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries);
        for (var i = 0; i < words.Length && i <= 3; i++)
        {
            var f = words[i];
            if (TimestampWord.IsMatch(f))
            {
                continue;
            }
            var word = f.Trim('[', ']', '<', '>', '(', ')', ':', '|');
            var n = Severity.Parse(word);
            if (n == 0)
            {
                return 0;
            }
            var marked = f.StartsWith('[') || f.StartsWith('<') || f.StartsWith('(') ||
                f.EndsWith(':') || f.EndsWith(']') || f.EndsWith('|');
            return marked || word == word.ToUpperInvariant() ? n : 0;
        }
        return 0;
    }

    /// <summary>
    /// Removes a leading upper-case level word that <see cref="TextLevel"/> read as
    /// <paramref name="sev"/> ("INFO started" → "started", "WARNING: disk" → "disk"): the level
    /// has its own column.
    /// </summary>
    public static string StripLevelWord(string s, int sev)
    {
        var space = s.IndexOf(' ');
        if (space < 0)
        {
            return s;
        }
        var word = s[..space];
        var rest = s[(space + 1)..];
        if (rest.Trim().Length == 0 || word != word.ToUpperInvariant())
        {
            return s;
        }
        var w = (word.EndsWith(':') ? word[..^1] : word).ToLowerInvariant();
        return w == Severity.Name(sev) || w == "warning" ? rest : s;
    }

    /// <summary>The trace id of a W3C traceparent header ("00-&lt;32 hex&gt;-&lt;16 hex&gt;-&lt;2 hex&gt;"), or null when there is none.</summary>
    public static string? TraceParentId(string? header)
    {
        var parts = (header ?? "").Trim().Split('-');
        if (parts.Length < 4 || parts[1].Length != 32 || parts[1].Trim('0').Length == 0)
        {
            return null;
        }
        foreach (var c in parts[1])
        {
            if (c is not (>= '0' and <= '9' or >= 'a' and <= 'f'))
            {
                return null;
            }
        }
        return parts[1];
    }
}

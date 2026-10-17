using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary;

/// <summary>
/// Scrubs credentials from headers and bodies before anything is stored. The body patterns
/// are regexes on purpose, so bodies cut off at the capture cap (invalid JSON) are still scrubbed.
/// </summary>
internal sealed class Redactor
{
    public const string Redacted = "[REDACTED]";

    private static readonly string[] DefaultHeaders =
        { "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token" };

    private static readonly string[] DefaultFields =
    {
        "password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
        "api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key",
    };

    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(1);

    private static readonly JsonWriterOptions JsonOptions = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };

    private readonly HashSet<string> _headers;
    private readonly Regex _responseJson;
    private readonly Regex _requestJson;
    private readonly Regex _form;

    public Redactor(IEnumerable<string> extraHeaders, IEnumerable<string> extraFields, IEnumerable<string> requestOnly)
    {
        _headers = new HashSet<string>(DefaultHeaders.Concat(extraHeaders).Select(h => h.Trim()), StringComparer.OrdinalIgnoreCase);
        var both = KeyPattern(DefaultFields.Concat(extraFields));
        var reqOnly = KeyPattern(requestOnly);
        var reqKeys = reqOnly.Length > 0 ? both + "|" + reqOnly : both;
        _responseJson = JsonSecrets(both);
        _requestJson = JsonSecrets(reqKeys);
        _form = new Regex("(?i)(^|&)(" + reqKeys + ")=[^&]*", RegexOptions.Compiled | RegexOptions.CultureInvariant, Timeout);
    }

    private static string KeyPattern(IEnumerable<string> names) =>
        string.Join("|", names.Select(n => n.Trim()).Where(n => n.Length > 0).Select(Regex.Escape));

    private static Regex JsonSecrets(string keys) =>
        new("(?i)(\"(?:" + keys + ")\"\\s*:\\s*)(\"(?:[^\"\\\\]|\\\\.)*\"?|[^,}\\]\\s]+)",
            RegexOptions.Compiled | RegexOptions.CultureInvariant, Timeout);

    /// <summary>A JSON object of the headers, multiple values joined with ", ", sensitive ones replaced.</summary>
    public string? Headers(IReadOnlyList<KeyValuePair<string, string>> headers)
    {
        if (headers.Count == 0)
        {
            return null;
        }
        using var buf = new MemoryStream();
        using (var w = new Utf8JsonWriter(buf, JsonOptions))
        {
            w.WriteStartObject();
            foreach (var (name, value) in headers)
            {
                w.WriteString(name, _headers.Contains(name) ? Redacted : value);
            }
            w.WriteEndObject();
        }
        return Encoding.UTF8.GetString(buf.ToArray());
    }

    public string? RequestBody(string? body, string? contentType)
    {
        if (string.IsNullOrEmpty(body))
        {
            return null;
        }
        if (contentType is not null && contentType.Contains("application/x-www-form-urlencoded", StringComparison.OrdinalIgnoreCase))
        {
            return Replace(_form, body, "${1}${2}=" + Redacted);
        }
        return Replace(_requestJson, body, "${1}\"" + Redacted + "\"");
    }

    public string? ResponseBody(string? body) =>
        string.IsNullOrEmpty(body) ? null : Replace(_responseJson, body, "${1}\"" + Redacted + "\"");

    /// <summary>A body that takes too long to scrub is not stored at all.</summary>
    private static string Replace(Regex re, string body, string replacement)
    {
        try
        {
            return re.Replace(body, replacement);
        }
        catch (RegexMatchTimeoutException)
        {
            return Redacted;
        }
    }
}

/// <summary>Reads error_code and error_key from JSON error responses.</summary>
internal static class ErrorFields
{
    /// <summary>
    /// The values at the dotted paths. Bodies that aren't valid JSON (for example cut off by the
    /// capture cap) fall back to a pattern match on the last path segment.
    /// </summary>
    public static (string? Code, string? Key) Read(string? body, string codePath, string keyPath)
    {
        if (string.IsNullOrEmpty(body))
        {
            return (null, null);
        }
        try
        {
            using var doc = JsonDocument.Parse(body);
            return (Lookup(doc.RootElement, codePath), Lookup(doc.RootElement, keyPath));
        }
        catch (JsonException)
        {
            return (Match(body, codePath), Match(body, keyPath));
        }
    }

    private static string? Lookup(JsonElement v, string path)
    {
        if (string.IsNullOrEmpty(path))
        {
            return null;
        }
        foreach (var seg in path.Split('.'))
        {
            if (v.ValueKind != JsonValueKind.Object || !v.TryGetProperty(seg, out v))
            {
                return null;
            }
        }
        return v.ValueKind switch
        {
            JsonValueKind.String => NullIfEmpty(v.GetString()),
            JsonValueKind.Number when v.TryGetInt64(out var n) => n.ToString(CultureInfo.InvariantCulture),
            JsonValueKind.Number => v.GetDouble().ToString("0.###############", CultureInfo.InvariantCulture),
            _ => null,
        };
    }

    private static string? Match(string body, string path)
    {
        if (string.IsNullOrEmpty(path))
        {
            return null;
        }
        var last = path[(path.LastIndexOf('.') + 1)..];
        try
        {
            var m = Regex.Match(body, "\"" + Regex.Escape(last) + "\"\\s*:\\s*\"([^\"]*)\"", RegexOptions.CultureInvariant, TimeSpan.FromSeconds(1));
            return m.Success ? NullIfEmpty(m.Groups[1].Value) : null;
        }
        catch (RegexMatchTimeoutException)
        {
            return null;
        }
    }

    private static string? NullIfEmpty(string? s) => string.IsNullOrEmpty(s) ? null : s;
}

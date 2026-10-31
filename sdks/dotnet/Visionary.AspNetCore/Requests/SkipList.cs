using System;
using System.Collections.Generic;
using System.Text.Json;

namespace Styropyr0.Visionary;

/// <summary>
/// Settings → Data, kept in visionary_settings as requests.skip: a JSON array of exact paths,
/// or prefixes ending in "*". A skipped request is treated like a path outside the prefix.
/// </summary>
internal static class SkipList
{
    public const string Setting = "requests.skip";
    public const string Query = "SELECT name, value FROM visionary_settings WHERE name LIKE 'requests.%'";

    public static IReadOnlyList<string> Parse(IEnumerable<string?[]> rows)
    {
        IReadOnlyList<string> list = Array.Empty<string>();
        foreach (var r in rows)
        {
            if (r.Length >= 2 && r[0] == Setting)
            {
                list = Parse(r[1]);
            }
        }
        return list;
    }

    // Anything but a JSON array of strings skips nothing.
    public static IReadOnlyList<string> Parse(string? value)
    {
        string?[]? raw;
        try
        {
            raw = JsonSerializer.Deserialize<string?[]>(value ?? "");
        }
        catch (JsonException)
        {
            return Array.Empty<string>();
        }
        var list = new List<string>();
        foreach (var p in raw ?? Array.Empty<string?>())
        {
            if (p?.Trim() is { Length: > 0 } t)
            {
                list.Add(t);
            }
        }
        return list;
    }

    public static bool Match(IReadOnlyList<string> list, string path)
    {
        foreach (var p in list)
        {
            if (p.EndsWith('*') ? path.StartsWith(p[..^1], StringComparison.Ordinal) : p == path)
            {
                return true;
            }
        }
        return false;
    }
}

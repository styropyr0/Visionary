using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;

namespace Styropyr0.Visionary.Profiling;

/// <summary>
/// What the dashboard asks for (Settings, Profiling), read from visionary_settings; see SPEC.md
/// "CPU profiles".
/// </summary>
internal sealed record ProfileSettings
{
    public const string Query = "SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'";

    /// <summary>A "Profile now" older than this is ignored (the service was down meanwhile).</summary>
    public static readonly TimeSpan NowFresh = TimeSpan.FromMinutes(5);

    public bool Enabled { get; init; }
    public IReadOnlyList<string> Services { get; init; } = Array.Empty<string>();
    public double IntervalS { get; init; } = 300;
    public double DurationMs { get; init; } = 10_000;
    public string Now { get; init; } = "";

    /// <summary>Settings from (name, value) rows; anything missing or unreadable takes its default.</summary>
    public static ProfileSettings Parse(IEnumerable<string?[]> rows)
    {
        var m = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var r in rows)
        {
            if (r.Length >= 2 && r[0] is { } name)
            {
                m[name] = r[1] ?? "";
            }
        }
        string Get(string k) => m.TryGetValue(k, out var v) ? v.Trim() : "";
        double Num(string k, double def) =>
            double.TryParse(Get(k), NumberStyles.Float, CultureInfo.InvariantCulture, out var n) && n > 0 && !double.IsInfinity(n) ? n : def;

        var services = new List<string>();
        try
        {
            using var doc = JsonDocument.Parse(Get("profile.services") is { Length: > 0 } s ? s : "[]");
            if (doc.RootElement.ValueKind == JsonValueKind.Array)
            {
                services.AddRange(doc.RootElement.EnumerateArray()
                    .Select(e => e.ValueKind == JsonValueKind.String ? e.GetString() ?? "" : e.GetRawText()));
            }
        }
        catch (JsonException)
        {
            // a broken list means every service
        }
        return new ProfileSettings
        {
            Enabled = Get("profile.enabled").Equals("true", StringComparison.OrdinalIgnoreCase),
            Services = services,
            IntervalS = Num("profile.interval_s", 300),
            DurationMs = Num("profile.duration_ms", 10_000),
            Now = Get("profile.now"),
        };
    }

    /// <summary>Whether a service takes part: an empty list means every service; no name matches only that.</summary>
    public bool Serves(string? service) =>
        Services.Count == 0 || (!string.IsNullOrEmpty(service) && Services.Contains(service, StringComparer.Ordinal));

    /// <summary>Whether a "Profile now" stamp asks for a profile: made after <paramref name="since"/> and less than 5 minutes ago.</summary>
    public static bool FreshNow(string stamp, DateTime since, DateTime now)
    {
        if (!DateTime.TryParse(stamp, CultureInfo.InvariantCulture,
                DateTimeStyles.AdjustToUniversal | DateTimeStyles.AssumeUniversal, out var at))
        {
            return false;
        }
        return at >= since && now - at < NowFresh;
    }
}

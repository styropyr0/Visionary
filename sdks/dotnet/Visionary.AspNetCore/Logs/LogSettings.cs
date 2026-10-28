using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>Severities as stored in app_logs.severity, with their level names.</summary>
internal static class Severity
{
    public const int Trace = 1;
    public const int Debug = 2;
    public const int Info = 3;
    public const int Warn = 4;
    public const int Error = 5;
    public const int Fatal = 6;

    private static readonly string[] Names = { "", "trace", "debug", "info", "warn", "error", "fatal" };

    public static string Name(int sev) => sev is >= Trace and <= Fatal ? Names[sev] : "info";

    /// <summary>
    /// Reads a level name the way every source spells them: trace, debug, info, notice, warn,
    /// warning, error, err, critical, fatal, panic... Unknown names are 0.
    /// </summary>
    public static int Parse(string? s) => (s ?? "").Trim().ToLowerInvariant() switch
    {
        "trace" or "trc" or "finest" or "finer" or "verbose" or "vrb" => Trace,
        "debug" or "dbg" or "fine" or "config" => Debug,
        "info" or "inf" or "information" or "informational" or "notice" or "log" => Info,
        "warn" or "warning" or "wrn" => Warn,
        "error" or "err" or "eror" or "severe" => Error,
        "fatal" or "critical" or "crit" or "alert" or "emerg" or "emergency" or "panic" or "ftl" => Fatal,
        _ => 0,
    };

    /// <summary>Microsoft.Extensions.Logging's levels; None is 0 (never stored).</summary>
    public static int From(LogLevel level) => level switch
    {
        LogLevel.Trace => Trace,
        LogLevel.Debug => Debug,
        LogLevel.Information => Info,
        LogLevel.Warning => Warn,
        LogLevel.Error => Error,
        LogLevel.Critical => Fatal,
        _ => 0,
    };
}

/// <summary>Changes the level for part of the traffic until a time; empty fields match anything.</summary>
internal sealed record LogOverride(string Service, string Route, string User, int Level, DateTime? Until)
{
    /// <summary>Whether it applies now to <paramref name="service"/>.</summary>
    public bool Active(string? service, DateTime now) =>
        (Until is null || now <= Until) && (Service.Length == 0 || Service == (service ?? ""));

    /// <summary>Whether it applies to a request on <paramref name="route"/> by <paramref name="user"/>.</summary>
    public bool Matches(string? route, string? user) =>
        (Route.Length == 0 || RouteMatches(Route, route ?? "")) && (User.Length == 0 || User == user);

    /// <summary>A route pattern, or a prefix ending in *.</summary>
    public static bool RouteMatches(string pattern, string route) =>
        pattern.EndsWith('*') ? route.StartsWith(pattern[..^1], StringComparison.Ordinal) : pattern == route;
}

/// <summary>Drops the lines it matches: every non-empty field must match; Level drops lines at or below it.</summary>
internal sealed record LogDrop(string Service, string Logger, string Contains, int Level);

/// <summary>
/// What the dashboard keeps (Settings → Logs), read from visionary_settings; see SPEC.md
/// "What is kept".
/// </summary>
internal sealed record LogSettings
{
    public const string Query = "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'";

    public bool Enabled { get; init; } = true;
    public int Level { get; init; } = Severity.Info;
    public bool Buffer { get; init; } = true;
    public int BufferLevel { get; init; } = Severity.Debug;
    public double SlowMs { get; init; } = 1000;
    public int Rate { get; init; } = 6000;
    public IReadOnlyList<LogOverride> Overrides { get; init; } = Array.Empty<LogOverride>();
    public IReadOnlyList<LogDrop> Drops { get; init; } = Array.Empty<LogDrop>();

    /// <summary>The defaults, with <paramref name="level"/> (VISIONARY_LOG_LEVEL) as the level.</summary>
    public static LogSettings Defaults(int level) => new() { Level = level > 0 ? level : Severity.Info };

    /// <summary>Settings from (name, value) rows of logs.*; anything missing or unreadable keeps its default.</summary>
    public static LogSettings Parse(IEnumerable<string?[]> rows, int baseLevel)
    {
        var s = Defaults(baseLevel);
        foreach (var r in rows)
        {
            if (r.Length < 2 || r[0] is not { } name)
            {
                continue;
            }
            var v = (r[1] ?? "").Trim();
            switch (name)
            {
                case "logs.enabled" when Bool(v) is { } b:
                    s = s with { Enabled = b };
                    break;
                case "logs.level" when Severity.Parse(v) is > 0 and var n:
                    s = s with { Level = n };
                    break;
                case "logs.buffer" when Bool(v) is { } b:
                    s = s with { Buffer = b };
                    break;
                case "logs.buffer_level" when Severity.Parse(v) is > 0 and var n:
                    s = s with { BufferLevel = n };
                    break;
                case "logs.slow_ms" when double.TryParse(v, NumberStyles.Float, CultureInfo.InvariantCulture, out var ms) && ms > 0 && !double.IsInfinity(ms):
                    s = s with { SlowMs = ms };
                    break;
                case "logs.rate" when int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out var rate) && rate >= 0:
                    s = s with { Rate = rate };
                    break;
                case "logs.overrides":
                    s = s with { Overrides = ParseOverrides(v) };
                    break;
                case "logs.drop":
                    s = s with { Drops = ParseDrops(v) };
                    break;
            }
        }
        return s;
    }

    private static bool? Bool(string v) => v.ToLowerInvariant() switch
    {
        "true" or "1" or "yes" => true,
        "false" or "0" or "no" => false,
        _ => null,
    };

    private static List<LogOverride> ParseOverrides(string json)
    {
        var list = new List<LogOverride>();
        foreach (var o in Objects(json))
        {
            var level = Severity.Parse(Field(o, "level"));
            if (level == 0)
            {
                continue;
            }
            DateTime? until = null;
            var u = Field(o, "until");
            if (u.Length > 0)
            {
                if (!DateTime.TryParse(u, CultureInfo.InvariantCulture,
                        DateTimeStyles.AdjustToUniversal | DateTimeStyles.AssumeUniversal, out var t))
                {
                    continue;
                }
                until = t;
            }
            list.Add(new LogOverride(Field(o, "service"), Field(o, "route"), Field(o, "user"), level, until));
        }
        return list;
    }

    private static List<LogDrop> ParseDrops(string json)
    {
        var list = new List<LogDrop>();
        foreach (var o in Objects(json))
        {
            var d = new LogDrop(Field(o, "service"), Field(o, "logger"), Field(o, "contains"), Severity.Parse(Field(o, "level")));
            if (d.Service.Length > 0 || d.Logger.Length > 0 || d.Contains.Length > 0 || d.Level > 0)
            {
                list.Add(d);
            }
        }
        return list;
    }

    /// <summary>The objects of a JSON array; nothing when it isn't one.</summary>
    private static List<Dictionary<string, string>> Objects(string json)
    {
        var list = new List<Dictionary<string, string>>();
        try
        {
            using var doc = JsonDocument.Parse(json);
            if (doc.RootElement.ValueKind != JsonValueKind.Array)
            {
                return list;
            }
            foreach (var e in doc.RootElement.EnumerateArray())
            {
                if (e.ValueKind != JsonValueKind.Object)
                {
                    continue;
                }
                var m = new Dictionary<string, string>(StringComparer.Ordinal);
                foreach (var p in e.EnumerateObject())
                {
                    m[p.Name] = p.Value.ValueKind switch
                    {
                        JsonValueKind.String => p.Value.GetString() ?? "",
                        JsonValueKind.Null => "",
                        _ => p.Value.GetRawText(),
                    };
                }
                list.Add(m);
            }
        }
        catch (JsonException)
        {
            // a broken list means none
        }
        return list;
    }

    private static string Field(Dictionary<string, string> o, string key) => o.TryGetValue(key, out var v) ? v.Trim() : "";

    /// <summary>
    /// The lowest level kept outright for a line outside any request, or once its request is
    /// known: the base level, then service-wide overrides, then the most specific matching
    /// override (a user beats a route). Later overrides win among equals.
    /// </summary>
    public int Floor(string? service, string? route, string? user, DateTime now)
    {
        int level = Level, rank = 0;
        foreach (var o in Overrides)
        {
            if (!o.Active(service, now) || !o.Matches(route, user))
            {
                continue;
            }
            var r = o.User.Length > 0 ? 3 : o.Route.Length > 0 ? 2 : 1;
            if (r >= rank)
            {
                level = o.Level;
                rank = r;
            }
        }
        return level;
    }

    /// <summary>
    /// The level under which a line logged in a request is held until the request ends: the
    /// highest level a service-wide, route or user override could give the request.
    /// </summary>
    public int HoldBelow(string? service, DateTime now)
    {
        var level = Level;
        foreach (var o in Overrides)
        {
            if (o.Active(service, now) && o.Route.Length == 0 && o.User.Length == 0)
            {
                level = o.Level;
            }
        }
        foreach (var o in Overrides)
        {
            if (o.Active(service, now) && (o.Route.Length > 0 || o.User.Length > 0))
            {
                level = Math.Max(level, o.Level);
            }
        }
        return level;
    }

    /// <summary>
    /// The lowest level any line could be kept at now: below it the logger reports itself
    /// disabled, so the application doesn't even format those messages.
    /// </summary>
    public int Lowest(string? service, DateTime now)
    {
        if (!Enabled)
        {
            return int.MaxValue;
        }
        var level = Level;
        if (Buffer)
        {
            level = Math.Min(level, BufferLevel);
        }
        foreach (var o in Overrides)
        {
            if (o.Active(service, now))
            {
                level = Math.Min(level, o.Level);
            }
        }
        return level;
    }

    /// <summary>Whether a drop rule matches the line.</summary>
    public bool Dropped(string? service, string? logger, string message, int sev)
    {
        foreach (var d in Drops)
        {
            if ((d.Service.Length > 0 && d.Service != (service ?? "")) ||
                (d.Logger.Length > 0 && d.Logger != logger) ||
                (d.Level > 0 && sev > d.Level) ||
                (d.Contains.Length > 0 && !message.Contains(d.Contains, StringComparison.Ordinal)))
            {
                continue;
            }
            return true;
        }
        return false;
    }
}

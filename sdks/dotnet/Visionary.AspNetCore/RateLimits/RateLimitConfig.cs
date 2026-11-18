using System;
using System.Collections.Generic;
using System.Text.Json;

namespace Styropyr0.Visionary;

/// <summary>
/// The ratelimit.config document from Security → Rate limits. See "Rate limits" in
/// sdks/SPEC.md; sdks/go/ratelimit is the reference.
/// </summary>
internal sealed class RateLimitConfig
{
    public const string SettingVersion = "ratelimit.version";
    public const string SettingConfig = "ratelimit.config";

    public bool? Enabled { get; set; }
    public string IpHeader { get; set; } = "";
    public List<string> CountryHeaders { get; set; } = new();
    public List<RateLimitRule> Rules { get; set; } = new();

    public bool On => Enabled ?? true;

    /// <summary>
    /// Reads a stored document leniently: a field or rule of the wrong shape is dropped, the
    /// rest kept. Only invalid JSON throws.
    /// </summary>
    public static RateLimitConfig Parse(string raw)
    {
        using var doc = JsonDocument.Parse(raw);
        var c = new RateLimitConfig();
        if (doc.RootElement.ValueKind != JsonValueKind.Object)
        {
            return c;
        }
        foreach (var p in doc.RootElement.EnumerateObject())
        {
            switch (p.Name.ToLowerInvariant())
            {
                case "enabled":
                    if (Json.Bool(p.Value, out var on))
                    {
                        c.Enabled = on;
                    }
                    break;
                case "ip_header":
                    if (Json.Str(p.Value, out var h))
                    {
                        c.IpHeader = h;
                    }
                    break;
                case "country_headers":
                    if (Json.Strings(p.Value, out var list))
                    {
                        c.CountryHeaders = list;
                    }
                    break;
                case "rules":
                    if (p.Value.ValueKind == JsonValueKind.Array)
                    {
                        c.Rules = new List<RateLimitRule>();
                        foreach (var r in p.Value.EnumerateArray())
                        {
                            if (RateLimitRule.Read(r) is { } rule)
                            {
                                c.Rules.Add(rule);
                            }
                        }
                    }
                    break;
            }
        }
        return c;
    }
}

/// <summary>One rule as stored. Zero values mean the field is missing.</summary>
internal sealed class RateLimitRule
{
    public string Id { get; set; } = "";
    public string Name { get; set; } = "";
    public bool? Enabled { get; set; }
    public string Mode { get; set; } = "";
    public string Action { get; set; } = "";
    public List<string> Services { get; set; } = new();
    public List<string> Methods { get; set; } = new();
    public List<string> Paths { get; set; } = new();
    public List<string> Ips { get; set; } = new();
    public List<string> Countries { get; set; } = new();
    public Dictionary<string, List<string>> Match { get; set; } = new();
    public string Key { get; set; } = "";
    public double Limit { get; set; }
    public double WindowS { get; set; }
    public string Algorithm { get; set; } = "";
    public double Burst { get; set; }
    public string LimitBy { get; set; } = "";
    public Dictionary<string, double> Limits { get; set; } = new();
    public int Status { get; set; }
    public string Body { get; set; } = "";
    public string ContentType { get; set; } = "";

    public bool On => Enabled ?? true;

    // Null when a field has the wrong type, as a Go decode would fail.
    internal static RateLimitRule? Read(JsonElement e)
    {
        if (e.ValueKind == JsonValueKind.Null)
        {
            return new RateLimitRule();
        }
        if (e.ValueKind != JsonValueKind.Object)
        {
            return null;
        }
        var r = new RateLimitRule();
        foreach (var p in e.EnumerateObject())
        {
            var v = p.Value;
            var ok = true;
            switch (p.Name.ToLowerInvariant())
            {
                case "id": ok = Json.Str(v, out var id); r.Id = id; break;
                case "name": ok = Json.Str(v, out var name); r.Name = name; break;
                case "enabled":
                    ok = Json.Bool(v, out var on);
                    r.Enabled = v.ValueKind == JsonValueKind.Null ? null : on;
                    break;
                case "mode": ok = Json.Str(v, out var mode); r.Mode = mode; break;
                case "action": ok = Json.Str(v, out var action); r.Action = action; break;
                case "services": ok = Json.Strings(v, out var services); r.Services = services; break;
                case "methods": ok = Json.Strings(v, out var methods); r.Methods = methods; break;
                case "paths": ok = Json.Strings(v, out var paths); r.Paths = paths; break;
                case "ips": ok = Json.Strings(v, out var ips); r.Ips = ips; break;
                case "countries": ok = Json.Strings(v, out var countries); r.Countries = countries; break;
                case "match":
                    r.Match = new Dictionary<string, List<string>>();
                    if (v.ValueKind == JsonValueKind.Object)
                    {
                        foreach (var m in v.EnumerateObject())
                        {
                            ok &= Json.Strings(m.Value, out var values);
                            r.Match[m.Name] = values;
                        }
                    }
                    else
                    {
                        ok = v.ValueKind == JsonValueKind.Null;
                    }
                    break;
                case "key": ok = Json.Str(v, out var key); r.Key = key; break;
                case "limit": ok = Json.Num(v, out var limit); r.Limit = limit; break;
                case "window_s": ok = Json.Num(v, out var window); r.WindowS = window; break;
                case "algorithm": ok = Json.Str(v, out var alg); r.Algorithm = alg; break;
                case "burst": ok = Json.Num(v, out var burst); r.Burst = burst; break;
                case "limit_by": ok = Json.Str(v, out var by); r.LimitBy = by; break;
                case "limits":
                    r.Limits = new Dictionary<string, double>();
                    if (v.ValueKind == JsonValueKind.Object)
                    {
                        foreach (var m in v.EnumerateObject())
                        {
                            ok &= Json.Num(m.Value, out var n);
                            r.Limits[m.Name] = n;
                        }
                    }
                    else
                    {
                        ok = v.ValueKind == JsonValueKind.Null;
                    }
                    break;
                case "status":
                    if (v.ValueKind == JsonValueKind.Number)
                    {
                        ok = v.TryGetInt32(out var status);
                        r.Status = status;
                    }
                    else
                    {
                        ok = v.ValueKind == JsonValueKind.Null;
                    }
                    break;
                case "body": ok = Json.Str(v, out var body); r.Body = body; break;
                case "content_type": ok = Json.Str(v, out var ct); r.ContentType = ct; break;
            }
            if (!ok)
            {
                return null;
            }
        }
        return r;
    }
}

// JSON null reads as the zero value; any other wrong type fails.
file static class Json
{
    public static bool Str(JsonElement v, out string s)
    {
        s = "";
        switch (v.ValueKind)
        {
            case JsonValueKind.String:
                s = v.GetString() ?? "";
                return true;
            case JsonValueKind.Null:
                return true;
            default:
                return false;
        }
    }

    public static bool Bool(JsonElement v, out bool b)
    {
        b = false;
        switch (v.ValueKind)
        {
            case JsonValueKind.True:
                b = true;
                return true;
            case JsonValueKind.False:
            case JsonValueKind.Null:
                return true;
            default:
                return false;
        }
    }

    public static bool Num(JsonElement v, out double n)
    {
        n = 0;
        if (v.ValueKind == JsonValueKind.Null)
        {
            return true;
        }
        return v.ValueKind == JsonValueKind.Number && v.TryGetDouble(out n) && double.IsFinite(n);
    }

    public static bool Strings(JsonElement v, out List<string> list)
    {
        list = new List<string>();
        if (v.ValueKind == JsonValueKind.Null)
        {
            return true;
        }
        if (v.ValueKind != JsonValueKind.Array)
        {
            return false;
        }
        foreach (var item in v.EnumerateArray())
        {
            if (!Str(item, out var s))
            {
                return false;
            }
            list.Add(s);
        }
        return true;
    }
}

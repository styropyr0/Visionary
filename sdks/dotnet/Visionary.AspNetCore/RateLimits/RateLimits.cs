using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Threading;

namespace Styropyr0.Visionary;

/// <summary>What the limiter sees of a request. Header and Query return the first value, "" when absent.</summary>
internal sealed class LimitRequest
{
    public string Method { get; init; } = "GET";
    public string Path { get; init; } = "/";
    public string RemoteIp { get; init; } = "";
    public Func<string, string>? Header { get; init; }
    public Func<string, string>? Query { get; init; }
}

internal readonly record struct LimitUser(string? Id, string? Role);

/// <summary>The answer for a request that is limited or blocked.</summary>
internal sealed record LimitDenial(int Status, string RuleId, int RetryAfter, string Body, string ContentType);

/// <summary>Carries one request between the two phases.</summary>
internal sealed class LimitState
{
    internal RateLimits.RuleSet? Set;
    internal string Ip = "";
    internal string Country = "";
    internal bool Allowed;
    internal bool UserDone;
    internal LimitDenial? Denial;

    public LimitDenial? Denied => Denial;
}

/// <summary>One rate_limit_events row.</summary>
internal sealed class LimitEvent
{
    public DateTime CreatedAt { get; init; }
    public string RuleId { get; init; } = "";
    public string RuleName { get; init; } = "";
    public string Outcome { get; init; } = "";
    public string KeyValue { get; init; } = "";
    public string Ip { get; init; } = "";
    public string Country { get; init; } = "";
    public string Method { get; init; } = "";
    public string Path { get; init; } = "";
    public string UserId { get; init; } = "";
    public long Hits { get; set; }
}

/// <summary>
/// Evaluates the rules in force and keeps the counters, per process. Thread-safe; anything
/// going wrong lets the request through.
/// </summary>
internal sealed class RateLimits
{
    public const string Limited = "limited";
    public const string Blocked = "blocked";
    public const string WouldLimit = "would_limit";
    public const string WouldBlock = "would_block";

    public const int MaxWindow = 86400;
    public const int MaxCounters = 10000;
    public const int MaxAggregates = 1000;

    public static readonly string[] DefaultCountryHeaders =
        { "CF-IPCountry", "CloudFront-Viewer-Country", "X-Vercel-IP-Country", "Fastly-Geo-Country", "X-AppEngine-Country", "X-Country-Code" };

    private static readonly string[] DefaultRedactHeaders =
        { "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token" };

    private readonly string _service;
    private readonly object _loadLock = new();
    private Dictionary<string, string> _shapes = new();
    private RuleSet? _set;
    private volatile HashSet<string> _redact = new();
    private readonly object _aggLock = new();
    private Dictionary<string, LimitEvent> _agg = new();
    private List<string> _order = new();

    /// <summary>When set, sees every recorded outcome (tests).</summary>
    internal Action<string, string>? Trace;

    public RateLimits(string? service)
    {
        _service = service ?? "";
        SetRedactHeaders(DefaultRedactHeaders);
    }

    /// <summary>Names the headers whose values are never recorded as keys.</summary>
    public void SetRedactHeaders(IEnumerable<string> names)
    {
        var set = new HashSet<string>(StringComparer.Ordinal);
        foreach (var n in names)
        {
            set.Add(n.Trim().ToLowerInvariant());
        }
        _redact = set;
    }

    /// <summary>Puts a config in force, keeping the counters of rules whose key, window, algorithm and burst did not change.</summary>
    public void Load(RateLimitConfig c)
    {
        lock (_loadLock)
        {
            var next = Compile(c, _service);
            var old = Volatile.Read(ref _set);
            var shapes = new Dictionary<string, string>();
            foreach (var (id, r) in next.ById)
            {
                shapes[id] = r.Shape();
                if (old is not null && _shapes.TryGetValue(id, out var was) && was == shapes[id] && old.ById.TryGetValue(id, out var prev))
                {
                    r.Counters = prev.Counters;
                }
                else
                {
                    r.Counters = new CounterSet();
                }
            }
            _shapes = shapes;
            Volatile.Write(ref _set, next);
        }
    }

    public void Clear() => Load(new RateLimitConfig());

    public bool Active => Volatile.Read(ref _set) is { ById.Count: > 0 };

    /// <summary>Runs the request phase; the state goes to <see cref="CheckUser"/> later.</summary>
    public LimitState CheckRequest(LimitRequest req, DateTime now)
    {
        var set = Volatile.Read(ref _set);
        if (set is null || set.ById.Count == 0)
        {
            return new LimitState();
        }
        try
        {
            var st = new LimitState { Set = set };
            st.Ip = ClientIp(req, set.IpHeader);
            st.Country = CountryOf(req, set.CountryHeaders);
            Run(set.Request, st, req, null, null, now);
            return st;
        }
        catch (Exception)
        {
            return new LimitState();
        }
    }

    /// <summary>Runs the user phase once per request; later calls repeat the first outcome.</summary>
    public LimitDenial? CheckUser(LimitState? st, LimitRequest req, LimitUser user, IReadOnlyDictionary<string, string>? attrs, DateTime now)
    {
        if (st?.Set is null)
        {
            return null;
        }
        lock (st)
        {
            if (st.UserDone || st.Allowed || st.Denial is not null)
            {
                return st.Denial;
            }
            st.UserDone = true;
            if (st.Set.User.Count == 0)
            {
                return null;
            }
            try
            {
                Run(st.Set.User, st, req, user, attrs, now);
                return st.Denial;
            }
            catch (Exception)
            {
                return null;
            }
        }
    }

    private void Run(List<Rule> rules, LimitState st, LimitRequest req, LimitUser? user, IReadOnlyDictionary<string, string>? attrs, DateTime now)
    {
        var t = Seconds(now);
        foreach (var r in rules)
        {
            if (!Matches(r, st, req, user, attrs))
            {
                continue;
            }
            switch (r.Action)
            {
                case "allow":
                    if (!r.Monitor)
                    {
                        st.Allowed = true;
                        return;
                    }
                    break;
                case "block":
                    if (r.Monitor)
                    {
                        Record(r, WouldBlock, "", st, req, user, now);
                        continue;
                    }
                    Record(r, Blocked, "", st, req, user, now);
                    st.Denial = r.Deny(0);
                    return;
                case "limit":
                    var key = Value(r.Key, st, req, user, attrs);
                    if (key.Length == 0)
                    {
                        continue;
                    }
                    var limit = r.Limit;
                    if (r.LimitBy is { } by && r.Limits.TryGetValue(Value(by, st, req, user, attrs), out var n))
                    {
                        limit = n;
                    }
                    if (limit < 0)
                    {
                        continue;
                    }
                    var (retry, over) = r.Hit(key, limit, t);
                    if (!over)
                    {
                        continue;
                    }
                    if (r.Monitor)
                    {
                        Record(r, WouldLimit, key, st, req, user, now);
                        continue;
                    }
                    Record(r, Limited, key, st, req, user, now);
                    st.Denial = r.Deny(retry);
                    return;
            }
        }
    }

    private static bool Matches(Rule r, LimitState st, LimitRequest req, LimitUser? user, IReadOnlyDictionary<string, string>? attrs)
    {
        if (r.Methods.Count > 0 && !r.Methods.Contains(req.Method.ToUpperInvariant()))
        {
            return false;
        }
        if (r.Paths.Count > 0)
        {
            var ok = false;
            foreach (var g in r.Paths)
            {
                if (Glob(g, req.Path))
                {
                    ok = true;
                    break;
                }
            }
            if (!ok)
            {
                return false;
            }
        }
        if (r.HasIps && !InPrefixes(r.Prefixes, st.Ip))
        {
            return false;
        }
        if (r.Countries.Count > 0 && (st.Country.Length == 0 || !r.Countries.Contains(st.Country)))
        {
            return false;
        }
        foreach (var (p, values) in r.Match)
        {
            var v = Value(p, st, req, user, attrs);
            if (values.Count == 0)
            {
                if (v.Length == 0)
                {
                    return false;
                }
            }
            else if (!values.Contains(v))
            {
                return false;
            }
        }
        return true;
    }

    private static string Value(Prop p, LimitState st, LimitRequest req, LimitUser? user, IReadOnlyDictionary<string, string>? attrs) => p.Kind switch
    {
        PropKind.Ip => st.Ip,
        PropKind.Country => st.Country,
        PropKind.Method => req.Method.ToUpperInvariant(),
        PropKind.Path => req.Path,
        PropKind.Header => req.Header?.Invoke(p.Name) ?? "",
        PropKind.Query => req.Query?.Invoke(p.Name) ?? "",
        PropKind.Global => "*",
        PropKind.User => user?.Id ?? "",
        PropKind.Role => user?.Role ?? "",
        PropKind.Attr => attrs is not null && attrs.TryGetValue(p.Name, out var a) ? a ?? "" : "",
        _ => "",
    };

    /// <summary>Drops idle counters of every rule; called at each poll.</summary>
    public void Sweep(DateTime now)
    {
        var set = Volatile.Read(ref _set);
        if (set is null)
        {
            return;
        }
        var t = Seconds(now);
        foreach (var r in set.ById.Values)
        {
            lock (r.Counters)
            {
                r.SweepLocked(t);
            }
        }
    }

    private void Record(Rule r, string outcome, string key, LimitState st, LimitRequest req, LimitUser? user, DateTime now)
    {
        Trace?.Invoke(r.Id, outcome);
        if (key.Length > 0 && r.Key.Kind == PropKind.Header && _redact.Contains(r.Key.Name))
        {
            key = "sha256:" + Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(key))).ToLowerInvariant()[..12];
        }
        key = Clip(key, 200);
        var ip = st.Ip;
        lock (_aggLock)
        {
            var id = r.Id + "\0" + outcome + "\0" + key + "\0" + ip;
            _agg.TryGetValue(id, out var e);
            if (e is null && _agg.Count >= MaxAggregates)
            {
                key = "(other)";
                ip = "";
                id = r.Id + "\0" + outcome + "\0" + key + "\0";
                _agg.TryGetValue(id, out e);
            }
            if (e is null)
            {
                e = new LimitEvent
                {
                    CreatedAt = now,
                    RuleId = r.Id,
                    RuleName = r.Name,
                    Outcome = outcome,
                    KeyValue = key,
                    Ip = ip,
                    Country = st.Country,
                    Method = req.Method.ToUpperInvariant(),
                    Path = Clip(req.Path, 300),
                    UserId = user?.Id ?? "",
                };
                _agg[id] = e;
                _order.Add(id);
            }
            e.Hits++;
        }
    }

    /// <summary>The outcomes recorded since the last call, oldest first.</summary>
    public List<LimitEvent> Drain()
    {
        lock (_aggLock)
        {
            var output = new List<LimitEvent>(_order.Count);
            foreach (var id in _order)
            {
                output.Add(_agg[id]);
            }
            _agg = new Dictionary<string, LimitEvent>();
            _order = new List<string>();
            return output;
        }
    }

    internal static double Seconds(DateTime now) => (now.ToUniversalTime() - DateTime.UnixEpoch).Ticks / 1e7;

    private static string ClientIp(LimitRequest req, string header)
    {
        if (header.Length > 0 && req.Header?.Invoke(header) is { Length: > 0 } v)
        {
            var comma = v.IndexOf(',');
            var first = (comma < 0 ? v : v[..comma]).Trim();
            if (first.Length > 0)
            {
                return first;
            }
        }
        return req.RemoteIp;
    }

    private static string CountryOf(LimitRequest req, IReadOnlyList<string> headers)
    {
        if (req.Header is null)
        {
            return "";
        }
        foreach (var h in headers)
        {
            var v = req.Header(h).Trim();
            if (v.Length > 0)
            {
                return v.ToUpperInvariant();
            }
        }
        return "";
    }

    /// <summary>Matches a whole path against a pattern where * is any run of characters.</summary>
    internal static bool Glob(string pattern, string s)
    {
        int p = 0, i = 0, star = -1, mark = 0;
        while (i < s.Length)
        {
            if (p < pattern.Length && pattern[p] == '*')
            {
                star = p;
                mark = i;
                p++;
            }
            else if (p < pattern.Length && pattern[p] == s[i])
            {
                p++;
                i++;
            }
            else if (star >= 0)
            {
                p = star + 1;
                mark++;
                i = mark;
            }
            else
            {
                return false;
            }
        }
        while (p < pattern.Length && pattern[p] == '*')
        {
            p++;
        }
        return p == pattern.Length;
    }

    private static string Clip(string s, int n)
    {
        if (s.Length <= n)
        {
            return s;
        }
        var b = new StringBuilder();
        var count = 0;
        foreach (var r in s.EnumerateRunes())
        {
            if (count++ == n)
            {
                return b.ToString();
            }
            b.Append(r.ToString());
        }
        return s;
    }

    // IPs and CIDRs

    internal readonly record struct Prefix(byte[] Bytes, int Bits);

    private static bool ParseAddr(string s, out byte[] bytes)
    {
        bytes = Array.Empty<byte>();
        s = s.Trim();
        var isV6 = s.Contains(':');
        if (!isV6)
        {
            // IPAddress also takes short forms like "1.2"; only dotted quads are IPs here
            var parts = s.Split('.');
            if (parts.Length != 4)
            {
                return false;
            }
            foreach (var part in parts)
            {
                if (part.Length is 0 or > 3 || (part.Length > 1 && part[0] == '0'))
                {
                    return false;
                }
                foreach (var ch in part)
                {
                    if (ch is < '0' or > '9')
                    {
                        return false;
                    }
                }
            }
        }
        if (!IPAddress.TryParse(s, out var a) || (isV6 != (a.AddressFamily == AddressFamily.InterNetworkV6)))
        {
            return false;
        }
        if (a.IsIPv4MappedToIPv6)
        {
            a = a.MapToIPv4();
        }
        bytes = a.GetAddressBytes();
        return true;
    }

    internal static bool ParsePrefix(string s, out Prefix prefix)
    {
        prefix = default;
        s = s.Trim();
        var slash = s.IndexOf('/');
        if (slash < 0)
        {
            if (!ParseAddr(s, out var b))
            {
                return false;
            }
            prefix = new Prefix(b, b.Length * 8);
            return true;
        }
        var addr = s[..slash];
        var bitsText = s[(slash + 1)..];
        if (addr.Contains('%') || bitsText.Length is 0 or > 3 || (bitsText.Length > 1 && bitsText[0] == '0')
            || !int.TryParse(bitsText, NumberStyles.None, CultureInfo.InvariantCulture, out var bits))
        {
            return false;
        }
        var isV6 = addr.Contains(':');
        if (!IPAddress.TryParse(addr.Trim(), out var a) || addr != addr.Trim() || !ParseAddr(addr, out _))
        {
            return false;
        }
        var bytes = a.GetAddressBytes();
        if (bits > bytes.Length * 8)
        {
            return false;
        }
        if (isV6 && a.IsIPv4MappedToIPv6 && bits >= 96)
        {
            bytes = a.MapToIPv4().GetAddressBytes();
            bits -= 96;
        }
        for (var i = 0; i < bytes.Length; i++)
        {
            var keep = Math.Clamp(bits - i * 8, 0, 8);
            bytes[i] &= (byte)(0xFF << (8 - keep));
        }
        prefix = new Prefix(bytes, bits);
        return true;
    }

    private static bool InPrefixes(List<Prefix> list, string ip)
    {
        if (!ParseAddr(ip, out var a))
        {
            return false;
        }
        foreach (var p in list)
        {
            if (p.Bytes.Length != a.Length)
            {
                continue;
            }
            var ok = true;
            for (var i = 0; i < a.Length && ok; i++)
            {
                var keep = Math.Clamp(p.Bits - i * 8, 0, 8);
                var mask = (byte)(0xFF << (8 - keep));
                ok = (a[i] & mask) == p.Bytes[i];
            }
            if (ok)
            {
                return true;
            }
        }
        return false;
    }

    // Rules

    internal enum PropKind { Ip, Country, Method, Path, Header, Query, Global, User, Role, Attr }

    internal readonly record struct Prop(PropKind Kind, string Name)
    {
        public bool UserPhase => Kind is PropKind.User or PropKind.Role or PropKind.Attr;
    }

    internal static bool ParseProp(string s, out Prop p)
    {
        foreach (var (prefix, kind) in new[] { ("header:", PropKind.Header), ("query:", PropKind.Query), ("attr:", PropKind.Attr) })
        {
            if (s.StartsWith(prefix, StringComparison.Ordinal) && s.Length > prefix.Length)
            {
                var name = s[prefix.Length..];
                p = new Prop(kind, kind == PropKind.Header ? name.ToLowerInvariant() : name);
                return true;
            }
        }
        PropKind? k = s switch
        {
            "ip" => PropKind.Ip,
            "country" => PropKind.Country,
            "method" => PropKind.Method,
            "path" => PropKind.Path,
            "global" => PropKind.Global,
            "user" => PropKind.User,
            "role" => PropKind.Role,
            _ => null,
        };
        p = new Prop(k ?? PropKind.Ip, "");
        return k is not null;
    }

    internal static bool ValidId(string id)
    {
        if (id.Length is 0 or > 32)
        {
            return false;
        }
        foreach (var c in id)
        {
            if (!(c is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9' or '_' or '-'))
            {
                return false;
            }
        }
        return true;
    }

    internal enum Algorithm { Sliding, Fixed, Bucket }

    internal sealed class RuleSet
    {
        public string IpHeader = "";
        public IReadOnlyList<string> CountryHeaders = DefaultCountryHeaders;
        public readonly List<Rule> Request = new();
        public readonly List<Rule> User = new();
        public readonly Dictionary<string, Rule> ById = new(StringComparer.Ordinal);
    }

    private static RuleSet Compile(RateLimitConfig c, string service)
    {
        var rs = new RuleSet { IpHeader = c.IpHeader.Trim() };
        var headers = new List<string>();
        foreach (var h in c.CountryHeaders)
        {
            if (h.Trim() is { Length: > 0 } t)
            {
                headers.Add(t);
            }
        }
        if (headers.Count > 0)
        {
            rs.CountryHeaders = headers;
        }
        if (!c.On)
        {
            return rs;
        }
        foreach (var src in c.Rules)
        {
            var r = Rule.Compile(src);
            if (r is null || !src.On || rs.ById.ContainsKey(r.Id) || !ServesService(src.Services, service))
            {
                continue;
            }
            rs.ById[r.Id] = r;
            (r.UserPhase ? rs.User : rs.Request).Add(r);
        }
        return rs;
    }

    private static bool ServesService(List<string> list, string service)
    {
        if (list.Count == 0)
        {
            return true;
        }
        return service.Length > 0 && list.Contains(service);
    }

    internal sealed class Counter
    {
        public long Win = long.MinValue;
        public double Curr;
        public double Prev;
        public double Tokens;
        public double Last;
    }

    internal sealed class CounterSet
    {
        public Dictionary<string, Counter> Map = new(StringComparer.Ordinal);
    }

    internal sealed class Rule
    {
        public string Id = "";
        public string Name = "";
        public bool Monitor;
        public string Action = "";
        public readonly List<string> Methods = new();
        public List<string> Paths = new();
        public bool HasIps;
        public readonly List<Prefix> Prefixes = new();
        public readonly List<string> Countries = new();
        public readonly List<(Prop, List<string>)> Match = new();
        public Prop Key;
        public double Limit;
        public double Window;
        public Algorithm Alg;
        public double Burst;
        public Prop? LimitBy;
        public Dictionary<string, double> Limits = new();
        public int Status;
        public string Body = "";
        public string ContentType = "";
        public bool UserPhase;
        public CounterSet Counters = new();

        // What the counters depend on; a change resets them.
        public string Shape() => string.Join("|", Key.Name, (int)Key.Kind, Window.ToString("R", CultureInfo.InvariantCulture),
            (int)Alg, Burst.ToString("R", CultureInfo.InvariantCulture));

        public static Rule? Compile(RateLimitRule src)
        {
            if (!ValidId(src.Id))
            {
                return null;
            }
            var r = new Rule
            {
                Id = src.Id,
                Name = src.Name.Length > 0 ? src.Name : src.Id,
                Action = src.Action,
                Status = src.Status,
                Body = src.Body,
                ContentType = src.ContentType,
            };
            switch (src.Mode)
            {
                case "" or "enforce":
                    break;
                case "monitor":
                    r.Monitor = true;
                    break;
                default:
                    return null;
            }
            if (src.Action is not ("limit" or "block" or "allow"))
            {
                return null;
            }
            foreach (var m in src.Methods)
            {
                r.Methods.Add(m.Trim().ToUpperInvariant());
            }
            r.Paths = src.Paths;
            r.HasIps = src.Ips.Count > 0;
            foreach (var s in src.Ips)
            {
                if (ParsePrefix(s, out var p))
                {
                    r.Prefixes.Add(p);
                }
            }
            foreach (var c in src.Countries)
            {
                r.Countries.Add(c.Trim().ToUpperInvariant());
            }
            foreach (var (name, values) in src.Match)
            {
                if (!ParseProp(name, out var p) || p.Kind == PropKind.Global)
                {
                    return null;
                }
                r.Match.Add((p, values));
                r.UserPhase |= p.UserPhase;
            }
            if (src.Action != "limit")
            {
                return r;
            }
            if (!ParseProp(src.Key.Length > 0 ? src.Key : "ip", out var k))
            {
                return null;
            }
            r.Key = k;
            r.UserPhase |= k.UserPhase;
            if (src.WindowS < 1 || src.WindowS > MaxWindow)
            {
                return null;
            }
            r.Window = src.WindowS;
            r.Limit = src.Limit;
            r.Burst = src.Burst;
            switch (src.Algorithm)
            {
                case "" or "sliding":
                    r.Alg = Algorithm.Sliding;
                    break;
                case "fixed":
                    r.Alg = Algorithm.Fixed;
                    break;
                case "token_bucket":
                    r.Alg = Algorithm.Bucket;
                    break;
                default:
                    return null;
            }
            if (src.LimitBy.Length > 0)
            {
                if (!ParseProp(src.LimitBy, out var by) || by.Kind == PropKind.Global)
                {
                    return null;
                }
                r.LimitBy = by;
                r.Limits = src.Limits;
                r.UserPhase |= by.UserPhase;
            }
            return r;
        }

        public LimitDenial Deny(int retry)
        {
            var status = Status != 0 ? Status : Action == "block" ? 403 : 429;
            string body, type;
            if (Body.Length > 0)
            {
                body = Body.Replace("{retry_after}", retry.ToString(CultureInfo.InvariantCulture), StringComparison.Ordinal);
                var trimmed = Body.TrimStart(' ', '\t', '\r', '\n');
                type = trimmed.StartsWith('{') || trimmed.StartsWith('[') ? "application/json" : "text/plain; charset=utf-8";
            }
            else if (Action == "block")
            {
                body = "{\"error\":{\"code\":\"blocked\",\"key\":\"" + Id + "\",\"message\":\"Forbidden\"}}";
                type = "application/json";
            }
            else
            {
                body = "{\"error\":{\"code\":\"rate_limited\",\"key\":\"" + Id + "\",\"message\":\"Too many requests\"}}";
                type = "application/json";
            }
            if (ContentType.Length > 0)
            {
                type = ContentType;
            }
            return new LimitDenial(status, Id, retry, body, type);
        }

        private double Capacity(double limit) => Burst > 0 ? Burst : limit;

        /// <summary>Takes one request on the key's counter: whether it is over, and the seconds to wait.</summary>
        public (int Retry, bool Over) Hit(string key, double limit, double t)
        {
            var w = Window;
            if (limit == 0)
            {
                return ((int)Math.Ceiling(w), true);
            }
            var cs = Counters;
            lock (cs)
            {
                if (!cs.Map.TryGetValue(key, out var c))
                {
                    if (cs.Map.Count >= MaxCounters)
                    {
                        SweepLocked(t);
                        if (cs.Map.Count >= MaxCounters)
                        {
                            cs.Map = new Dictionary<string, Counter>(StringComparer.Ordinal);
                        }
                    }
                    c = new Counter { Tokens = Capacity(limit), Last = t };
                    cs.Map[key] = c;
                }
                if (Alg == Algorithm.Bucket)
                {
                    double cap = Capacity(limit), rate = limit / w;
                    if (t > c.Last)
                    {
                        c.Tokens = Math.Min(cap, c.Tokens + (t - c.Last) * rate);
                        c.Last = t;
                    }
                    if (c.Tokens < 1)
                    {
                        return (Math.Max(1, (int)Math.Ceiling((1 - c.Tokens) / rate)), true);
                    }
                    c.Tokens--;
                    return (0, false);
                }
                var win = (long)Math.Floor(t / w);
                if (c.Win == long.MinValue)
                {
                    c.Prev = c.Curr = 0;
                }
                else if (win == c.Win + 1)
                {
                    c.Prev = c.Curr;
                    c.Curr = 0;
                }
                else if (win > c.Win + 1)
                {
                    c.Prev = c.Curr = 0;
                }
                c.Win = Math.Max(c.Win, win);
                bool over;
                if (Alg == Algorithm.Fixed)
                {
                    over = c.Curr >= limit;
                }
                else
                {
                    var e = t - win * w;
                    over = c.Prev * (w - e) / w + c.Curr + 1 > limit;
                }
                if (over)
                {
                    return (Math.Max(1, (int)Math.Ceiling((win + 1) * w - t)), true);
                }
                c.Curr++;
                return (0, false);
            }
        }

        // Drops idle counters; the caller holds the set's lock.
        public void SweepLocked(double t)
        {
            if (Counters.Map.Count == 0 || Window <= 0)
            {
                return;
            }
            var w = (long)Math.Floor(t / Window);
            var drop = new List<string>();
            foreach (var (k, c) in Counters.Map)
            {
                if (Alg == Algorithm.Bucket)
                {
                    // the limit may differ per key; the static one is close enough to call it idle
                    if (Limit <= 0 || c.Tokens + (t - c.Last) * (Limit / Window) >= Capacity(Limit))
                    {
                        drop.Add(k);
                    }
                }
                else if (c.Win < w - 1)
                {
                    drop.Add(k);
                }
            }
            foreach (var k in drop)
            {
                Counters.Map.Remove(k);
            }
        }
    }
}

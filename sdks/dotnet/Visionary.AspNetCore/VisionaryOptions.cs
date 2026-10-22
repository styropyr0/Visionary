using System;
using System.Collections.Generic;
using System.Data.Common;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Configuration;

namespace Styropyr0.Visionary;

/// <summary>
/// Settings for <c>AddVisionary</c>. Every value starts from the VISIONARY_* environment
/// variable (or the <see cref="IConfiguration"/> key of the same name) and can then be
/// overridden in code.
/// </summary>
public sealed class VisionaryOptions
{
    /// <summary>
    /// A data source the application already has (for example an <c>NpgsqlDataSource</c>);
    /// preferred over <see cref="DatabaseUrl"/>, since the app then owns drivers and pooling.
    /// </summary>
    public DbDataSource? DataSource { get; set; }

    /// <summary>Creates a new, unopened connection; an alternative to <see cref="DataSource"/>.</summary>
    public Func<DbConnection>? ConnectionFactory { get; set; }

    /// <summary>The SQL flavour of <see cref="DataSource"/> or <see cref="ConnectionFactory"/>; detected from the provider when null.</summary>
    public VisionaryDialect? Dialect { get; set; }

    /// <summary>VISIONARY_DATABASE_URL: postgres://, mysql://, sqlite:///path or d1://.</summary>
    public string? DatabaseUrl { get; set; }

    /// <summary>D1_ACCOUNT_ID.</summary>
    public string? D1AccountId { get; set; }

    /// <summary>D1_DATABASE_ID.</summary>
    public string? D1DatabaseId { get; set; }

    /// <summary>D1_API_TOKEN.</summary>
    public string? D1ApiToken { get; set; }

    /// <summary>D1_API_BASE: overrides https://api.cloudflare.com/client/v4.</summary>
    public string? D1ApiBase { get; set; }

    /// <summary>VISIONARY_INSTANCE: labels this server's rows; defaults to the hostname.</summary>
    public string? Instance { get; set; }

    /// <summary>VISIONARY_LOG_REQUESTS (default true).</summary>
    public bool LogRequests { get; set; } = true;

    /// <summary>VISIONARY_LOG_PATH_PREFIX: only paths starting with it are logged; empty logs everything.</summary>
    public string PathPrefix { get; set; } = "";

    /// <summary>VISIONARY_LOG_BODIES (default true): store redacted request and response bodies.</summary>
    public bool LogBodies { get; set; } = true;

    /// <summary>VISIONARY_LOG_MAX_BODY_BYTES (default 8192): the capture cap per body.</summary>
    public int MaxBodyBytes { get; set; } = 8192;

    /// <summary>VISIONARY_LOG_RETENTION_DAYS (default 30, 0 keeps forever): api_logs, spans and errors.</summary>
    public int LogRetentionDays { get; set; } = 30;

    /// <summary>VISIONARY_LOG_REDACT_HEADERS: added to the default redacted headers.</summary>
    public List<string> RedactHeaders { get; set; } = new();

    /// <summary>VISIONARY_LOG_REDACT_FIELDS: JSON/form keys redacted in request and response bodies.</summary>
    public List<string> RedactFields { get; set; } = new();

    /// <summary>VISIONARY_LOG_REDACT_REQUEST_FIELDS: keys redacted in request bodies only.</summary>
    public List<string> RedactRequestFields { get; set; } = new();

    /// <summary>VISIONARY_LOG_ERROR_CODE_FIELD (default error.code): dotted path into JSON error bodies.</summary>
    public string ErrorCodeField { get; set; } = "error.code";

    /// <summary>VISIONARY_LOG_ERROR_KEY_FIELD (default error.key).</summary>
    public string ErrorKeyField { get; set; } = "error.key";

    /// <summary>VISIONARY_SYSTEM_METRICS (default true).</summary>
    public bool SystemMetrics { get; set; } = true;

    /// <summary>VISIONARY_SYSTEM_INTERVAL (default 15s; a plain number is seconds, also 500ms, 1m).</summary>
    public TimeSpan SystemInterval { get; set; } = TimeSpan.FromSeconds(15);

    /// <summary>VISIONARY_SYSTEM_RETENTION_DAYS (default 30).</summary>
    public int SystemRetentionDays { get; set; } = 30;

    /// <summary>VISIONARY_SERVICE: lets several services share a database.</summary>
    public string? Service { get; set; }

    /// <summary>VISIONARY_RELEASE, else the platform's commit variable (Railway, Render, Vercel, Heroku, GitHub...).</summary>
    public string? Release { get; set; }

    /// <summary>VISIONARY_SPANS (default true): database and outbound HTTP spans and per-request totals.</summary>
    public bool Spans { get; set; } = true;

    /// <summary>VISIONARY_SPAN_THRESHOLD_MS (default 100): spans outside a request are kept when at least this slow.</summary>
    public double SpanThresholdMs { get; set; } = 100;

    /// <summary>VISIONARY_SPAN_MAX (default 100): span rows per request.</summary>
    public int SpanMax { get; set; } = 100;

    /// <summary>VISIONARY_ERRORS (default true): unhandled and captured exceptions.</summary>
    public bool Errors { get; set; } = true;

    /// <summary>
    /// Returns the route pattern for a request when endpoint routing has none, e.g. for
    /// terminal middleware. Called after the handler.
    /// </summary>
    public Func<HttpContext, string?>? Route { get; set; }

    /// <summary>
    /// Fills in handle and role for a user id, on the background writer, once per distinct user
    /// per batch and only for users missing either. Keep it cheap (a cache); return null when unknown.
    /// </summary>
    public Func<string, CancellationToken, ValueTask<VisionaryUser?>>? LookupUser { get; set; }

    /// <summary>Database pool statistics for the db_* columns of system_metrics; optional.</summary>
    public Func<VisionaryDbStats?>? DbStats { get; set; }

    /// <summary>The filesystem whose usage is reported; defaults to the one holding the working directory.</summary>
    public string? DiskPath { get; set; }

    private static readonly string[] CommitVariables =
    {
        "RAILWAY_GIT_COMMIT_SHA", "RENDER_GIT_COMMIT", "VERCEL_GIT_COMMIT_SHA", "HEROKU_SLUG_COMMIT",
        "SOURCE_VERSION", "GIT_COMMIT", "GITHUB_SHA", "K_REVISION",
    };

    /// <summary>Reads the VISIONARY_* and D1_* settings from configuration and the environment.</summary>
    internal void Load(IConfiguration? config)
    {
        string? Get(string key)
        {
            var v = config?[key];
            if (string.IsNullOrWhiteSpace(v))
            {
                v = Environment.GetEnvironmentVariable(key);
            }
            return string.IsNullOrWhiteSpace(v) ? null : v.Trim();
        }
        bool Bool(string key, bool def) => Get(key)?.ToLowerInvariant() switch
        {
            "true" or "1" or "yes" or "on" => true,
            "false" or "0" or "no" or "off" => false,
            _ => def,
        };
        int Int(string key, int def) =>
            int.TryParse(Get(key), NumberStyles.Integer, CultureInfo.InvariantCulture, out var n) && n >= 0 ? n : def;
        double Double(string key, double def) =>
            double.TryParse(Get(key), NumberStyles.Float, CultureInfo.InvariantCulture, out var n) && n >= 0 ? n : def;
        List<string> List(string key) =>
            (Get(key) ?? "").Split(',').Select(s => s.Trim()).Where(s => s.Length > 0).ToList();

        DatabaseUrl = Get("VISIONARY_DATABASE_URL");
        D1AccountId = Get("D1_ACCOUNT_ID");
        D1DatabaseId = Get("D1_DATABASE_ID");
        D1ApiToken = Get("D1_API_TOKEN");
        D1ApiBase = Get("D1_API_BASE");
        Instance = Get("VISIONARY_INSTANCE");
        LogRequests = Bool("VISIONARY_LOG_REQUESTS", true);
        PathPrefix = Get("VISIONARY_LOG_PATH_PREFIX") ?? "";
        LogBodies = Bool("VISIONARY_LOG_BODIES", true);
        MaxBodyBytes = Int("VISIONARY_LOG_MAX_BODY_BYTES", 8192);
        LogRetentionDays = Int("VISIONARY_LOG_RETENTION_DAYS", 30);
        RedactHeaders = List("VISIONARY_LOG_REDACT_HEADERS");
        RedactFields = List("VISIONARY_LOG_REDACT_FIELDS");
        RedactRequestFields = List("VISIONARY_LOG_REDACT_REQUEST_FIELDS");
        ErrorCodeField = Get("VISIONARY_LOG_ERROR_CODE_FIELD") ?? "error.code";
        ErrorKeyField = Get("VISIONARY_LOG_ERROR_KEY_FIELD") ?? "error.key";
        SystemMetrics = Bool("VISIONARY_SYSTEM_METRICS", true);
        SystemInterval = Duration(Get("VISIONARY_SYSTEM_INTERVAL"), TimeSpan.FromSeconds(15));
        SystemRetentionDays = Int("VISIONARY_SYSTEM_RETENTION_DAYS", 30);
        Service = Get("VISIONARY_SERVICE");
        Release = Get("VISIONARY_RELEASE") ?? CommitVariables.Select(Get).FirstOrDefault(v => v is not null);
        Spans = Bool("VISIONARY_SPANS", true);
        SpanThresholdMs = Double("VISIONARY_SPAN_THRESHOLD_MS", 100);
        SpanMax = Int("VISIONARY_SPAN_MAX", 100);
        Errors = Bool("VISIONARY_ERRORS", true);
    }

    /// <summary>A plain number is seconds; otherwise a Go-style duration such as 500ms, 15s, 1m or 1h30m.</summary>
    internal static TimeSpan Duration(string? v, TimeSpan def)
    {
        if (string.IsNullOrWhiteSpace(v))
        {
            return def;
        }
        if (double.TryParse(v, NumberStyles.Float, CultureInfo.InvariantCulture, out var secs))
        {
            return secs > 0 ? TimeSpan.FromSeconds(secs) : def;
        }
        var parts = Regex.Matches(v, @"(\d+(?:\.\d+)?)(ms|us|µs|ns|h|m|s)");
        if (parts.Count == 0 || string.Concat(parts.Select(p => p.Value)) != v)
        {
            return def;
        }
        double ms = 0;
        foreach (Match p in parts)
        {
            var n = double.Parse(p.Groups[1].Value, CultureInfo.InvariantCulture);
            ms += p.Groups[2].Value switch
            {
                "h" => n * 3_600_000,
                "m" => n * 60_000,
                "s" => n * 1000,
                "ms" => n,
                "us" or "µs" => n / 1000,
                _ => n / 1_000_000,
            };
        }
        return ms > 0 ? TimeSpan.FromMilliseconds(ms) : def;
    }

    /// <summary>The release as stored: commit hashes are shortened to 12 characters.</summary>
    internal string? Version()
    {
        var v = Release?.Trim();
        if (string.IsNullOrEmpty(v))
        {
            return null;
        }
        return v.Length > 12 && Regex.IsMatch(v, "^[0-9a-fA-F]{13,64}$") ? v[..12] : v;
    }
}

/// <summary>Who made a request. Only the id is required.</summary>
/// <param name="Id">The user id, stored as user_id.</param>
/// <param name="Handle">A display name or username, stored as user_handle.</param>
/// <param name="Role">A role such as admin, stored as user_role.</param>
public sealed record VisionaryUser(string Id, string? Handle = null, string? Role = null);

/// <summary>
/// A database pool snapshot. <see cref="Acquires"/>, <see cref="EmptyAcquires"/> and
/// <see cref="AcquireWaitMs"/> are cumulative; the sampler stores per-sample deltas.
/// </summary>
public sealed record VisionaryDbStats
{
    /// <summary>Open connections.</summary>
    public long Total { get; init; }
    /// <summary>Idle connections.</summary>
    public long Idle { get; init; }
    /// <summary>Connections in use.</summary>
    public long InUse { get; init; }
    /// <summary>The pool's maximum size.</summary>
    public long Max { get; init; }
    /// <summary>Connections handed out, cumulative.</summary>
    public long Acquires { get; init; }
    /// <summary>Acquires that had to wait for a connection, cumulative.</summary>
    public long EmptyAcquires { get; init; }
    /// <summary>Time spent waiting for connections, cumulative, in milliseconds.</summary>
    public double AcquireWaitMs { get; init; }
}

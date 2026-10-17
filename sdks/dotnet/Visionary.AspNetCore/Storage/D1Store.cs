using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace Styropyr0.Visionary;

/// <summary>
/// Cloudflare D1 over its REST API. Params are always strings (column affinity converts
/// numbers); statements use NULLIF(?, '') so "" becomes NULL.
/// </summary>
internal sealed class D1Store : IStore, IDisposable
{
    public const string DefaultApiBase = "https://api.cloudflare.com/client/v4";

    /// <summary>D1 accepts at most 50 statements per batch call.</summary>
    private const int MaxBatch = 50;

    /// <summary>Writes answer with tiny bodies; column listings are small too.</summary>
    private const int ResponseLimit = 1 << 20;

    private readonly HttpClient _http;
    private readonly string _queryUrl;
    private readonly string _rawUrl;

    public D1Store(string? apiBase, string accountId, string databaseId, string apiToken)
    {
        var b = string.IsNullOrWhiteSpace(apiBase) ? DefaultApiBase : apiBase.Trim().TrimEnd('/');
        var root = $"{b}/accounts/{Uri.EscapeDataString(accountId)}/d1/database/{Uri.EscapeDataString(databaseId)}";
        _queryUrl = root + "/query";
        _rawUrl = root + "/raw";
        // No trace headers on these calls: they are the SDK's own writes, not the app's traffic.
        var handler = new SocketsHttpHandler { ActivityHeadersPropagator = null, PooledConnectionLifetime = TimeSpan.FromMinutes(5) };
        _http = new HttpClient(handler) { Timeout = TimeSpan.FromSeconds(20) };
        _http.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue("Bearer", apiToken);
        Describe = $"Cloudflare D1 database {databaseId}";
    }

    public VisionaryDialect Dialect => VisionaryDialect.Sqlite;

    public bool StringParams => true;

    public string Describe { get; }

    public async Task ExecScriptAsync(string script, CancellationToken ct) =>
        await PostAsync(_queryUrl, Statement(script, Array.Empty<object?>()), ct).ConfigureAwait(false);

    public async Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct)
    {
        for (var i = 0; i < stmts.Count; i += MaxBatch)
        {
            var chunk = stmts.Skip(i).Take(MaxBatch).Select(s => Statement(s.Sql, s.Args)).ToArray();
            await PostAsync(_queryUrl, new Dictionary<string, object> { ["batch"] = chunk }, ct).ConfigureAwait(false);
        }
    }

    public async Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct)
    {
        using var doc = await PostAsync(_queryUrl, Statement(sql, args), ct).ConfigureAwait(false);
        var result = doc.RootElement.GetProperty("result");
        if (result.ValueKind == JsonValueKind.Array && result.GetArrayLength() > 0 &&
            result[0].TryGetProperty("meta", out var meta) &&
            meta.TryGetProperty("changes", out var changes) && changes.TryGetInt64(out var n))
        {
            return n;
        }
        return 0;
    }

    public async Task<List<string>> ColumnsAsync(string query, CancellationToken ct)
    {
        using var doc = await PostAsync(_rawUrl, Statement(query, Array.Empty<object?>()), ct).ConfigureAwait(false);
        var names = new List<string>();
        var result = doc.RootElement.GetProperty("result");
        if (result.ValueKind == JsonValueKind.Array && result.GetArrayLength() > 0 &&
            result[0].TryGetProperty("results", out var results) &&
            results.TryGetProperty("rows", out var rows) && rows.ValueKind == JsonValueKind.Array)
        {
            foreach (var row in rows.EnumerateArray())
            {
                if (row.ValueKind == JsonValueKind.Array && row.GetArrayLength() > 0)
                {
                    names.Add(row[0].ToString());
                }
            }
        }
        return names;
    }

    public void Dispose() => _http.Dispose();

    private static Dictionary<string, object> Statement(string sql, object?[] args)
    {
        var st = new Dictionary<string, object> { ["sql"] = sql };
        if (args.Length > 0)
        {
            st["params"] = args.Select(StoreExtensions.AsString).ToArray();
        }
        return st;
    }

    /// <summary>Posts a body and returns the envelope once D1 reports success.</summary>
    private async Task<JsonDocument> PostAsync(string url, object body, CancellationToken ct)
    {
        using var content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json");
        using var resp = await _http.PostAsync(url, content, ct).ConfigureAwait(false);
        var raw = await ReadLimitedAsync(resp.Content, ct).ConfigureAwait(false);
        JsonDocument doc;
        try
        {
            doc = JsonDocument.Parse(raw);
        }
        catch (JsonException)
        {
            throw new HttpRequestException($"d1: status {(int)resp.StatusCode}: unreadable response");
        }
        var root = doc.RootElement;
        var ok = root.ValueKind == JsonValueKind.Object && root.TryGetProperty("success", out var s) && s.ValueKind == JsonValueKind.True;
        if (!ok || (int)resp.StatusCode >= 300)
        {
            var msg = Messages(root);
            doc.Dispose();
            throw new HttpRequestException($"d1: status {(int)resp.StatusCode}: {msg}");
        }
        return doc;
    }

    private static async Task<byte[]> ReadLimitedAsync(HttpContent content, CancellationToken ct)
    {
        await using var stream = await content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        using var buf = new MemoryStream();
        var chunk = new byte[16 * 1024];
        int n;
        while ((n = await stream.ReadAsync(chunk, ct).ConfigureAwait(false)) > 0)
        {
            if (buf.Length + n > ResponseLimit)
            {
                throw new HttpRequestException($"d1: response larger than {ResponseLimit} bytes");
            }
            buf.Write(chunk, 0, n);
        }
        return buf.ToArray();
    }

    private static string Messages(JsonElement root)
    {
        if (root.ValueKind == JsonValueKind.Object && root.TryGetProperty("errors", out var errors) && errors.ValueKind == JsonValueKind.Array)
        {
            var parts = errors.EnumerateArray()
                .Select(e => e.ValueKind == JsonValueKind.Object && e.TryGetProperty("message", out var m) ? m.ToString() : e.ToString())
                .Where(m => m.Length > 0)
                .ToList();
            if (parts.Count > 0)
            {
                return string.Join("; ", parts);
            }
        }
        return "request failed";
    }
}

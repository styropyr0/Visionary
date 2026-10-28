using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>Turns queued items into typed INSERT rows, in the column orders of SPEC.md.</summary>
internal sealed class RowBuilder
{
    public const string TimestampFormat = "yyyy-MM-dd'T'HH:mm:ss.fff'Z'";

    private readonly VisionaryAgent _agent;
    private readonly string _requestInsert;
    private readonly string _spanInsert;
    private readonly string _errorInsert;
    private readonly string _releaseInsert;
    private readonly string _profileInsert;
    private readonly string _logInsert;

    public RowBuilder(VisionaryAgent agent)
    {
        _agent = agent;
        var strings = agent.Store?.StringParams ?? false;
        _requestInsert = Schema.InsertSql("api_logs", Schema.RequestColumns, strings);
        _spanInsert = Schema.InsertSql("spans", Schema.SpanColumns, strings);
        _errorInsert = Schema.InsertSql("errors", Schema.ErrorColumns, strings);
        _releaseInsert = Schema.InsertSql("releases", Schema.ReleaseColumns, strings);
        _profileInsert = Schema.InsertSql("profiles", Schema.ProfileColumns, strings);
        _logInsert = Schema.InsertSql("app_logs", Schema.LogColumns, strings);
    }

    public static string Timestamp(DateTime t) => t.ToUniversalTime().ToString(TimestampFormat, CultureInfo.InvariantCulture);

    public async Task<List<Stmt>> BuildAsync(List<WriteItem> batch, CancellationToken ct)
    {
        var users = await LookupUsersAsync(batch, ct).ConfigureAwait(false);
        var stmts = new List<Stmt>(batch.Count);
        foreach (var item in batch)
        {
            switch (item)
            {
                case RequestEntry e:
                    stmts.Add(new Stmt(_requestInsert, Request(e, Complete(e.User, users))));
                    foreach (var s in e.Spans)
                    {
                        stmts.Add(new Stmt(_spanInsert, Span(s)));
                    }
                    break;
                case SpanRecord s:
                    stmts.Add(new Stmt(_spanInsert, Span(s)));
                    break;
                case ErrorRecord r:
                    stmts.Add(new Stmt(_errorInsert, Error(r)));
                    break;
                case ReleaseRecord r:
                    stmts.Add(new Stmt(_releaseInsert, new object?[]
                    {
                        Timestamp(r.CreatedAt), _agent.Service, _agent.Instance, _agent.Version,
                        VisionaryAgent.Sdk, _agent.SdkVersion, _agent.Runtime,
                    }));
                    break;
                case ProfileRecord r:
                    stmts.Add(new Stmt(_profileInsert, new object?[]
                    {
                        Timestamp(r.CreatedAt), _agent.Service, _agent.Instance, _agent.Version,
                        VisionaryAgent.Sdk, Math.Round(r.DurationMs, 3), r.Samples, r.Routes, r.Folded,
                    }));
                    break;
            }
        }
        return stmts;
    }

    private object?[] Request(RequestEntry e, VisionaryUser? u)
    {
        var o = _agent.Options;
        string? code = null, key = null;
        if (e.Status >= 400)
        {
            (code, key) = ErrorFields.Read(e.ResponseBody, o.ErrorCodeField, o.ErrorKeyField);
        }
        var redact = _agent.Redactor;
        return new object?[]
        {
            e.RequestId,
            Timestamp(e.StartedAt),
            e.Method,
            e.Path,
            Null(e.Route),
            Null(e.Query),
            (long)e.Status,
            e.DurationMs,
            Null(e.Ip),
            Null(e.UserAgent),
            Null(u?.Id),
            Null(u?.Handle),
            Null(u?.Role),
            Null(redact.Headers(e.RequestHeaders)),
            Null(e.RequestContentType),
            o.LogBodies ? Null(redact.RequestBody(e.RequestBody, e.RequestContentType)) : null,
            e.RequestTruncated ? 1L : 0L,
            e.RequestSize,
            Null(e.ResponseContentType),
            o.LogBodies ? Null(redact.ResponseBody(e.ResponseBody)) : null,
            e.ResponseTruncated ? 1L : 0L,
            e.ResponseSize,
            Null(code),
            Null(key),
            _agent.Service,
            _agent.Version,
            e.Totals?.DbCount,
            e.Totals?.DbMs,
            e.Totals?.HttpCount,
            e.Totals?.HttpMs,
            Null(e.LoginAccount),
        };
    }

    public Stmt Log(LogRecord r, ISet<string> secrets) => new(_logInsert, LogRow(r, secrets));

    public object?[] LogRow(LogRecord r, ISet<string> secrets)
    {
        var message = LogText.Truncate(LogText.Scrub(r.Message), LogText.MaxMessage);
        var attrs = r.Attrs is { Count: > 0 } a ? LogText.EncodeAttrs(a, secrets) : null;
        return new object?[]
        {
            Timestamp(r.At),
            _agent.Service,
            _agent.Instance,
            _agent.Version,
            VisionaryAgent.Sdk,
            LogHost,
            null,
            Severity.Name(r.Sev),
            (long)r.Sev,
            Null(r.Logger),
            message,
            attrs,
            Null(r.RequestId),
            Null(r.TraceId),
            Null(r.SpanId),
            Null(r.UserId),
            Null(r.SessionId),
            null,
            null,
            null,
            (long)(Encoding.UTF8.GetByteCount(message) + (attrs is null ? 0 : Encoding.UTF8.GetByteCount(attrs))),
        };
    }

    private static readonly string LogHost = VisionaryAgent.HostName();

    private object?[] Span(SpanRecord s) => new object?[]
    {
        Timestamp(s.StartedAt),
        s.RequestId,
        _agent.Service,
        _agent.Instance,
        s.Kind,
        s.Name,
        Null(s.Target),
        Null(s.Detail),
        s.OffsetMs,
        s.DurationMs,
        s.Error is null ? "ok" : "error",
        Null(s.Error),
        s.RowCount,
    };

    private object?[] Error(ErrorRecord r) => new object?[]
    {
        Timestamp(r.CreatedAt),
        _agent.Service,
        _agent.Instance,
        _agent.Version,
        r.RequestId,
        Null(r.Method),
        Null(r.Route),
        Null(r.Path),
        Null(r.UserId),
        r.Type,
        Null(r.Message),
        Null(r.Stack),
        r.Fingerprint,
        r.Handled ? 1L : 0L,
        _agent.Runtime,
    };

    /// <summary>Resolves each distinct user missing a handle or role once per batch.</summary>
    private async Task<Dictionary<string, VisionaryUser?>> LookupUsersAsync(List<WriteItem> batch, CancellationToken ct)
    {
        var found = new Dictionary<string, VisionaryUser?>();
        var lookup = _agent.Options.LookupUser;
        if (lookup is null)
        {
            return found;
        }
        foreach (var item in batch)
        {
            if (item is not RequestEntry { User: { } u } || (u.Handle is not null && u.Role is not null) || found.ContainsKey(u.Id))
            {
                continue;
            }
            try
            {
                found[u.Id] = await lookup(u.Id, ct).ConfigureAwait(false);
            }
            catch (Exception e)
            {
                found[u.Id] = null;
                _agent.Logger.LogWarning("visionary: user lookup failed for {UserId}: {Error}", u.Id, e.Message);
            }
        }
        return found;
    }

    private static VisionaryUser? Complete(VisionaryUser? u, Dictionary<string, VisionaryUser?> found)
    {
        if (u is null || !found.TryGetValue(u.Id, out var full) || full is null)
        {
            return u;
        }
        return u with { Handle = u.Handle ?? full.Handle, Role = u.Role ?? full.Role };
    }

    private static string? Null(string? s) => string.IsNullOrEmpty(s) ? null : s;
}

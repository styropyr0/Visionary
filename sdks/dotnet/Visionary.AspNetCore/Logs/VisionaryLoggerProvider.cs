using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Copies <see cref="ILogger"/> output into Visionary, linked to the request it was logged in.
/// Register it with <c>builder.Logging.AddVisionary()</c>; other providers are unaffected.
/// </summary>
[ProviderAlias("Visionary")]
public sealed class VisionaryLoggerProvider : ILoggerProvider, ISupportExternalScope
{
    private IExternalScopeProvider? _scopes;

    /// <inheritdoc />
    public ILogger CreateLogger(string categoryName) =>
        Own(categoryName) ? NoLogger.Instance : new Logger(this, categoryName);

    /// <inheritdoc />
    public void SetScopeProvider(IExternalScopeProvider scopeProvider) => _scopes = scopeProvider;

    /// <inheritdoc />
    public void Dispose()
    {
    }

    // Never capture the SDK's own logs, or a failed write would queue behind itself.
    private static bool Own(string category) =>
        category == "Visionary" || category.StartsWith("Styropyr0.Visionary", StringComparison.Ordinal);

    private static AppLogs? Pipeline => VisionaryAgent.Current?.Logs;

    // From ActivityTrackingOptions scopes: trace and span get their own columns, the rest is noise.
    private static readonly HashSet<string> ActivityKeys = new(StringComparer.Ordinal)
    {
        "SpanId", "TraceId", "ParentId", "TraceState", "TraceFlags", "Tags", "Baggage",
    };

    private sealed class Logger : ILogger
    {
        private readonly VisionaryLoggerProvider _provider;
        private readonly string _category;

        public Logger(VisionaryLoggerProvider provider, string category)
        {
            _provider = provider;
            _category = category;
        }

        public IDisposable? BeginScope<TState>(TState state) where TState : notnull =>
            _provider._scopes?.Push(state);

        public bool IsEnabled(LogLevel logLevel) => Pipeline?.IsEnabled(Severity.From(logLevel)) == true;

        public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter)
        {
            var logs = Pipeline;
            var sev = Severity.From(logLevel);
            if (logs is null || !logs.IsEnabled(sev))
            {
                return;
            }
            try
            {
                var message = formatter(state, exception) ?? "";
                if (message.StartsWith("visionary: ", StringComparison.Ordinal))
                {
                    return;
                }
                if (exception is not null)
                {
                    message = message.Length > 0 ? message + "\n" + exception : exception.ToString();
                }
                logs.Capture(Record(sev, message, state));
            }
            catch (Exception)
            {
                // Logging must never become the application's error.
            }
        }

        private LogRecord Record<TState>(int sev, string message, TState state)
        {
            var r = new LogRecord { At = DateTime.UtcNow, Sev = sev, Message = message, Logger = _category };
            var attrs = new Dictionary<string, object>(StringComparer.Ordinal);
            string? scopeTrace = null, scopeSpan = null;
            List<string>? texts = null;
            // Outer scopes first, so inner ones and the line's own fields win.
            _provider._scopes?.ForEachScope((scope, _) =>
            {
                if (scope is IEnumerable<KeyValuePair<string, object?>> pairs)
                {
                    var list = pairs as IReadOnlyCollection<KeyValuePair<string, object?>> ?? new List<KeyValuePair<string, object?>>(pairs);
                    if (list.Count > 0 && AllActivityKeys(list))
                    {
                        foreach (var (k, v) in list)
                        {
                            if (k == "TraceId")
                            {
                                scopeTrace = v?.ToString();
                            }
                            else if (k == "SpanId")
                            {
                                scopeSpan = v?.ToString();
                            }
                        }
                        return;
                    }
                    AddPairs(attrs, list);
                }
                else if (scope is not null && Convert.ToString(scope, CultureInfo.InvariantCulture) is { Length: > 0 } text)
                {
                    (texts ??= new List<string>()).Add(text);
                }
            }, (object?)null);
            if (texts is not null)
            {
                attrs["scope"] = string.Join(" => ", texts);
            }
            if (state is IEnumerable<KeyValuePair<string, object?>> fields)
            {
                AddPairs(attrs, fields);
            }
            Take(r, attrs);
            if (r.TraceId is null && Activity.Current is { } activity && activity.TraceId != default)
            {
                r.TraceId = activity.TraceId.ToHexString();
                r.SpanId ??= activity.SpanId.ToHexString();
            }
            r.TraceId ??= scopeTrace;
            r.SpanId ??= scopeSpan;
            return r;
        }

        private static bool AllActivityKeys(IEnumerable<KeyValuePair<string, object?>> pairs)
        {
            foreach (var (k, _) in pairs)
            {
                if (!ActivityKeys.Contains(k))
                {
                    return false;
                }
            }
            return true;
        }
    }

    internal static void AddPairs(Dictionary<string, object> attrs, IEnumerable<KeyValuePair<string, object?>> pairs, string prefix = "", int depth = 0)
    {
        foreach (var (k, v) in pairs)
        {
            if (string.IsNullOrEmpty(k) || k == "{OriginalFormat}" || v is null)
            {
                continue;
            }
            if (v is IEnumerable<KeyValuePair<string, object?>> group && depth < 4)
            {
                AddPairs(attrs, group, prefix + k + ".", depth + 1);
                continue;
            }
            attrs[prefix + k] = Value(v);
        }
    }

    internal static object Value(object v) => v switch
    {
        string s => s,
        bool b => b,
        sbyte or short or int or long => Convert.ToInt64(v, CultureInfo.InvariantCulture),
        byte or ushort or uint => Convert.ToInt64(v, CultureInfo.InvariantCulture),
        ulong u => u,
        float or double or decimal => Convert.ToDouble(v, CultureInfo.InvariantCulture),
        DateTime t => (t.Kind == DateTimeKind.Local ? t.ToUniversalTime() : t).ToString("O", CultureInfo.InvariantCulture),
        DateTimeOffset t => t.ToString("O", CultureInfo.InvariantCulture),
        Enum e => e.ToString(),
        IEnumerable list => string.Join(", ", Items(list)),
        _ => Convert.ToString(v, CultureInfo.InvariantCulture) ?? "",
    };

    private static IEnumerable<string> Items(IEnumerable list)
    {
        foreach (var item in list)
        {
            yield return item is null ? "(null)" : Convert.ToString(item, CultureInfo.InvariantCulture) ?? "";
        }
    }

    // Moves keys that have their own columns (request_id, trace_id, user_id...) out of attrs.
    internal static void Take(LogRecord r, Dictionary<string, object> attrs)
    {
        foreach (var key in ColumnKeys)
        {
            if (!attrs.Remove(key, out var v))
            {
                continue;
            }
            var s = v as string ?? Convert.ToString(v, CultureInfo.InvariantCulture);
            if (string.IsNullOrEmpty(s))
            {
                continue;
            }
            switch (key)
            {
                case "request_id":
                    r.RequestId = s;
                    break;
                case "trace_id":
                    r.TraceId = s;
                    break;
                case "span_id":
                    r.SpanId = s;
                    break;
                case "user_id":
                    r.UserId = s;
                    break;
                case "session_id":
                    r.SessionId = s;
                    break;
                case "logger":
                    r.Logger = s;
                    break;
            }
        }
        if (attrs.Count > 0)
        {
            r.Attrs = attrs;
        }
    }

    private static readonly string[] ColumnKeys = { "request_id", "trace_id", "span_id", "user_id", "session_id", "logger" };

    private sealed class NoLogger : ILogger
    {
        public static readonly NoLogger Instance = new();

        public IDisposable? BeginScope<TState>(TState state) where TState : notnull => null;

        public bool IsEnabled(LogLevel logLevel) => false;

        public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter)
        {
        }
    }
}

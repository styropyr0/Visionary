using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Data.Common;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;

namespace Styropyr0.Visionary;

/// <summary>
/// Turns the runtime's diagnostics into spans: HttpClient through its DiagnosticListener,
/// Npgsql and MySqlConnector through their ActivitySources, and EF Core commands (any provider)
/// through EF's DiagnosticListener. EF commands on a provider that has its own source are left
/// to that source, so nothing is counted twice.
/// </summary>
internal sealed class SpanListeners : IHostedService, IObserver<DiagnosticListener>, IDisposable
{
    private const string HttpListenerName = "HttpHandlerDiagnosticListener";
    private const string EfListenerName = "Microsoft.EntityFrameworkCore";
    private const string EfCommandExecuted = "Microsoft.EntityFrameworkCore.Database.Command.CommandExecuted";
    private const string EfCommandError = "Microsoft.EntityFrameworkCore.Database.Command.CommandError";

    /// <summary>Driver ActivitySources by the assembly that holds their commands.</summary>
    private static readonly Dictionary<string, string> DriverSources = new()
    {
        ["Npgsql"] = "Npgsql",
        ["MySqlConnector"] = "MySqlConnector",
    };

    private readonly VisionaryAgent _agent;
    private readonly List<IDisposable> _subscriptions = new();
    private readonly ConcurrentDictionary<string, bool> _sourcesSeen = new();
    private readonly ConditionalWeakTable<HttpRequestMessage, HttpCall> _calls = new();
    private ActivityListener? _activities;

    public SpanListeners(VisionaryAgent agent) => _agent = agent;

    public Task StartAsync(CancellationToken cancellationToken)
    {
        if (!_agent.SpansOn)
        {
            return Task.CompletedTask;
        }
        _activities = new ActivityListener
        {
            ShouldListenTo = s => DriverSources.ContainsValue(s.Name),
            Sample = (ref ActivityCreationOptions<ActivityContext> _) =>
                Suppress.Active ? ActivitySamplingResult.None : ActivitySamplingResult.AllData,
            ActivityStopped = OnDriverActivity,
        };
        ActivitySource.AddActivityListener(_activities);
        lock (_subscriptions)
        {
            _subscriptions.Add(DiagnosticListener.AllListeners.Subscribe(this));
        }
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken)
    {
        Dispose();
        return Task.CompletedTask;
    }

    public void Dispose()
    {
        _activities?.Dispose();
        _activities = null;
        lock (_subscriptions)
        {
            foreach (var s in _subscriptions)
            {
                s.Dispose();
            }
            _subscriptions.Clear();
        }
    }

    public void OnNext(DiagnosticListener listener)
    {
        IDisposable? sub = listener.Name switch
        {
            HttpListenerName => listener.Subscribe(new Observer(OnHttpEvent), IsHttpEnabled),
            EfListenerName => listener.Subscribe(new Observer(OnEfEvent),
                (name, _, _) => !Suppress.Active && name is EfCommandExecuted or EfCommandError),
            _ => null,
        };
        if (sub is not null)
        {
            lock (_subscriptions)
            {
                _subscriptions.Add(sub);
            }
        }
    }

    public void OnCompleted() { }

    public void OnError(Exception error) { }

    // ---- HttpClient ----

    private sealed class HttpCall
    {
        public DateTime StartedAt;
        public long Started;
        public string? Error;
    }

    /// <summary>Returning false while suppressed means the SDK's own D1 calls get no activity at all.</summary>
    private static bool IsHttpEnabled(string name, object? a, object? b) =>
        !Suppress.Active && (name.StartsWith("System.Net.Http.HttpRequestOut", StringComparison.Ordinal) || name == "System.Net.Http.Exception");

    private void OnHttpEvent(KeyValuePair<string, object?> e)
    {
        try
        {
            switch (e.Key)
            {
                case "System.Net.Http.HttpRequestOut.Start":
                    if (Fetch.Get<HttpRequestMessage>(e.Value, "Request") is { } start)
                    {
                        _calls.AddOrUpdate(start, new HttpCall { StartedAt = DateTime.UtcNow, Started = Stopwatch.GetTimestamp() });
                    }
                    break;
                case "System.Net.Http.Exception":
                    if (Fetch.Get<HttpRequestMessage>(e.Value, "Request") is { } failed && _calls.TryGetValue(failed, out var call))
                    {
                        call.Error = Fetch.Get<Exception>(e.Value, "Exception")?.Message ?? "request failed";
                    }
                    break;
                case "System.Net.Http.HttpRequestOut.Stop":
                    OnHttpStop(e.Value);
                    break;
            }
        }
        catch (Exception)
        {
            // diagnostics must never break the application's HTTP call
        }
    }

    private void OnHttpStop(object? payload)
    {
        if (Fetch.Get<HttpRequestMessage>(payload, "Request") is not { RequestUri: { } uri } request ||
            !_calls.TryGetValue(request, out var call))
        {
            return;
        }
        _calls.Remove(request);
        var response = Fetch.Get<HttpResponseMessage>(payload, "Response");
        var error = call.Error;
        if (error is null && response is null && Fetch.Get<TaskStatus>(payload, "RequestTaskStatus") is TaskStatus.Faulted or TaskStatus.Canceled)
        {
            error = "request failed";
        }
        if (error is null && response is { StatusCode: var code } && (int)code >= 500)
        {
            error = $"HTTP {(int)code} {response.ReasonPhrase}".TrimEnd();
        }
        var method = request.Method.Method;
        var host = uri.IsDefaultPort ? uri.Host : $"{uri.Host}:{uri.Port}";
        var url = uri.GetLeftPart(UriPartial.Path);
        _agent.RecordSpan("http", $"{method} {uri.Host}", host, $"{method} {url}", call.StartedAt,
            Stopwatch.GetElapsedTime(call.Started).TotalMilliseconds, error, null);
    }

    // ---- Npgsql / MySqlConnector ----

    private void OnDriverActivity(Activity a)
    {
        try
        {
            if (Suppress.Active)
            {
                return;
            }
            _sourcesSeen[a.Source.Name] = true;
            var sql = Tag(a, "db.query.text") ?? Tag(a, "db.statement");
            if (string.IsNullOrEmpty(sql))
            {
                return; // connection opens and other non-command activities
            }
            var system = Tag(a, "db.system.name") ?? Tag(a, "db.system") ?? a.Source.Name.ToLowerInvariant();
            var database = Tag(a, "db.namespace") ?? Tag(a, "db.name");
            var error = a.Status == ActivityStatusCode.Error
                ? a.StatusDescription ?? Tag(a, "exception.message") ?? Tag(a, "error.type") ?? "error"
                : null;
            long? rows = long.TryParse(Tag(a, "db.response.returned_rows") ?? Tag(a, "db.rows_affected"), out var n) ? n : null;
            Record(sql, database is null ? system : $"{system}/{database}", a.StartTimeUtc, a.Duration.TotalMilliseconds, error, rows);
        }
        catch (Exception)
        {
            // diagnostics must never break the application's query
        }
    }

    private static string? Tag(Activity a, string name) => a.GetTagItem(name)?.ToString();

    // ---- EF Core ----

    private void OnEfEvent(KeyValuePair<string, object?> e)
    {
        try
        {
            if (Fetch.Get<DbCommand>(e.Value, "Command") is not { } cmd)
            {
                return;
            }
            var assembly = cmd.GetType().Assembly.GetName().Name ?? "";
            if (DriverSources.TryGetValue(assembly, out var source) && _sourcesSeen.ContainsKey(source))
            {
                return; // the driver's own activity already recorded it
            }
            var duration = Fetch.Get<TimeSpan>(e.Value, "Duration");
            var startedAt = Fetch.Get<DateTimeOffset>(e.Value, "StartTime");
            var start = startedAt == default ? DateTime.UtcNow - duration : startedAt.UtcDateTime;
            string? error = null;
            long? rows = null;
            if (e.Key == EfCommandError)
            {
                error = Fetch.Get<Exception>(e.Value, "Exception")?.Message ?? "error";
            }
            else
            {
                rows = Fetch.Get<object>(e.Value, "Result") switch
                {
                    int affected when affected >= 0 => affected,
                    DbDataReader { RecordsAffected: >= 0 } r => r.RecordsAffected,
                    _ => null,
                };
            }
            Record(cmd.CommandText, Target(cmd), start, duration.TotalMilliseconds, error, rows);
        }
        catch (Exception)
        {
            // diagnostics must never break the application's query
        }
    }

    /// <summary>System and database, e.g. postgresql/voygram or sqlite/app.db.</summary>
    private static string Target(DbCommand cmd)
    {
        var system = SqlText.System(cmd.GetType());
        var conn = cmd.Connection;
        if (conn is null)
        {
            return system;
        }
        var database = system == "sqlite" ? Path.GetFileName(conn.DataSource) : conn.Database;
        return string.IsNullOrEmpty(database) ? system : $"{system}/{database}";
    }

    private void Record(string sql, string target, DateTime startedAt, double durationMs, string? error, long? rows)
    {
        var clean = SqlText.Sanitize(sql);
        _agent.RecordSpan("db", SqlText.Name(clean), target, clean, startedAt, durationMs, error, rows);
    }

    private sealed class Observer : IObserver<KeyValuePair<string, object?>>
    {
        private readonly Action<KeyValuePair<string, object?>> _onNext;

        public Observer(Action<KeyValuePair<string, object?>> onNext) => _onNext = onNext;

        public void OnNext(KeyValuePair<string, object?> value) => _onNext(value);

        public void OnCompleted() { }

        public void OnError(Exception error) { }
    }

    /// <summary>Reads a property of a diagnostic payload, whose types are internal to their libraries.</summary>
    private static class Fetch
    {
        private static readonly ConcurrentDictionary<(Type, string), PropertyInfo?> Properties = new();

        public static T? Get<T>(object? payload, string name)
        {
            if (payload is null)
            {
                return default;
            }
            var prop = Properties.GetOrAdd((payload.GetType(), name), k => k.Item1.GetProperty(k.Item2, BindingFlags.Public | BindingFlags.Instance));
            return prop?.GetValue(payload) is T v ? v : default;
        }
    }
}

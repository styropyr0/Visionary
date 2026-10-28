using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Channels;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

// Application logs (app_logs): lines the application logs through ILogger (see
// VisionaryLoggerProvider), linked to the request whose async flow they were logged on.
//
// What is kept follows the dashboard (Settings → Logs), read from visionary_settings every 30
// seconds (AppLogWriter):
//
//   - logs.level: lines below it are not stored...
//   - ...except inside a request that failed (5xx, an exception, a captured error) or was slow
//     (logs.slow_ms): there the lines down to logs.buffer_level that were held back are written
//     too, so every failure comes with its debug lines (logs.buffer).
//   - logs.overrides lower or raise the level for a service, a route or a user until a time,
//     without a redeploy.
//   - logs.drop drops matching lines; logs.rate caps lines per minute.
//
// Lines below the level are held in the request until it ends and then written or dropped;
// nothing on a request path waits for the database.

/// <summary>One captured line. Scrubbing and encoding happen on the writer.</summary>
internal sealed class LogRecord
{
    public required DateTime At { get; init; }
    public required int Sev { get; init; }
    public required string Message { get; init; }
    public string? Logger { get; set; }
    public Dictionary<string, object>? Attrs { get; set; }
    public string? RequestId { get; set; }
    public string? TraceId { get; set; }
    public string? SpanId { get; set; }
    public string? UserId { get; set; }
    public string? SessionId { get; set; }
}

/// <summary>
/// A request's log state, flowing with its async context like <see cref="RequestState"/> but
/// kept for every request the middleware sees (logged or not): its ids, and the lines below the
/// level held until it ends.
/// </summary>
internal sealed class RequestLogs
{
    private static readonly AsyncLocal<RequestLogs?> CurrentLogs = new();

    private int _failed;

    public RequestLogs(HttpContext? context, string? traceId)
    {
        Context = context;
        TraceId = traceId;
    }

    /// <summary>The request this async flow belongs to, also after it ended (work it left running).</summary>
    public static RequestLogs? Current => CurrentLogs.Value;

    public HttpContext? Context { get; }

    /// <summary>The request_id, when the request is logged.</summary>
    public string? RequestId { get; set; }

    /// <summary>The trace id of the request's traceparent header.</summary>
    public string? TraceId { get; }

    /// <summary>Guards the fields below.</summary>
    public object Lock { get; } = new();

    public Queue<LogRecord>? Held { get; set; }
    public bool Done { get; set; }
    public string? Route { get; set; }
    public string? UserId { get; set; }

    /// <summary>An error was captured in the request, so its held lines count as a failure's.</summary>
    public bool Failed => Volatile.Read(ref _failed) == 1;

    public void MarkFailed() => Volatile.Write(ref _failed, 1);

    public void Enter() => CurrentLogs.Value = this;
}

/// <summary>The log pipeline: decides each line against the dashboard's settings and queues it for <see cref="AppLogWriter"/>.</summary>
internal sealed class AppLogs
{
    public const int QueueSize = 10_000;

    /// <summary>The lines one request holds back; past it, the oldest are dropped.</summary>
    public const int MaxHeld = 500;

    private readonly VisionaryAgent _agent;
    private Func<DateTime> _clock;
    private LogSettings _settings;
    private long _dropped;

    // The per-minute cap: lines counted in the current minute, and those dropped by it,
    // reported once the minute ends.
    private readonly object _rateLock = new();
    private long _rateMinute = long.MinValue;
    private int _rateCount;
    private int _rateDrop;

    public AppLogs(VisionaryAgent agent)
    {
        _agent = agent;
        _clock = () => DateTime.UtcNow;
        BaseLevel = Severity.Parse(agent.Options.LogLevel) is > 0 and var n ? n : Severity.Info;
        _settings = LogSettings.Defaults(BaseLevel);
        Secrets = LogText.SecretKeys(agent.Options.RedactFields);
        Queue = Channel.CreateBounded<LogRecord>(new BoundedChannelOptions(QueueSize)
        {
            FullMode = BoundedChannelFullMode.Wait,
            SingleReader = true,
        });
    }

    /// <summary>VISIONARY_LOG_LEVEL: the level until Settings → Logs has been read.</summary>
    public int BaseLevel { get; }

    /// <summary>The attribute names whose values are redacted.</summary>
    public HashSet<string> Secrets { get; }

    public Channel<LogRecord> Queue { get; }

    public LogSettings Settings
    {
        get => Volatile.Read(ref _settings);
        set => Volatile.Write(ref _settings, value);
    }

    /// <summary>For tests: the clock the overrides' expiry and the per-minute cap use.</summary>
    internal Func<DateTime> Clock
    {
        set => _clock = value;
    }

    /// <summary>Whether a line at <paramref name="sev"/> could be kept at all right now.</summary>
    public bool IsEnabled(int sev) => sev > 0 && sev >= Settings.Lowest(_agent.Service, _clock());

    public long TakeDropped() => Interlocked.Exchange(ref _dropped, 0);

    /// <summary>Takes one line logged on the current async flow. It never blocks.</summary>
    public void Capture(LogRecord r)
    {
        var s = Settings;
        // The SDK's own messages and work are left out: a write failure logged here would
        // otherwise be queued behind the write that failed.
        if (!s.Enabled || Suppress.Active || r.Message.StartsWith("visionary: ", StringComparison.Ordinal))
        {
            return;
        }
        var service = _agent.Service;
        if (s.Dropped(service, r.Logger, r.Message, r.Sev))
        {
            return;
        }
        var req = RequestLogs.Current;
        if (req is null)
        {
            if (r.Sev >= s.Floor(service, null, null, r.At))
            {
                Emit(r);
            }
            return;
        }
        r.RequestId ??= req.RequestId;
        r.TraceId ??= req.TraceId;
        string? route, user;
        bool done;
        lock (req.Lock)
        {
            done = req.Done;
            if (!done)
            {
                if (r.Sev < s.HoldBelow(service, r.At))
                {
                    var held = req.Held ??= new Queue<LogRecord>();
                    if (held.Count >= MaxHeld)
                    {
                        held.Dequeue();
                    }
                    held.Enqueue(r);
                    return;
                }
                route = null;
                user = null;
            }
            else
            {
                route = req.Route;
                user = req.UserId;
            }
        }
        if (!done)
        {
            r.UserId ??= CurrentUser(req);
            Emit(r);
            return;
        }
        // Work the request left running: its route and user are known now.
        r.UserId ??= user;
        if (r.Sev >= s.Floor(service, route, user, r.At))
        {
            Emit(r);
        }
    }

    /// <summary>
    /// Decides a request's held lines once it has ended: kept when they reach the level for its
    /// route and user, or, when the request failed or was slow, the buffer's level.
    /// </summary>
    public void Finish(RequestLogs req, string? route, string? user, bool failed, double durationMs)
    {
        Queue<LogRecord>? held;
        lock (req.Lock)
        {
            req.Done = true;
            req.Route = route;
            req.UserId = user;
            held = req.Held;
            req.Held = null;
        }
        if (held is null || held.Count == 0)
        {
            return;
        }
        var s = Settings;
        var floor = s.Floor(_agent.Service, route, user, _clock());
        var keepAll = s.Buffer && (failed || req.Failed || durationMs >= s.SlowMs);
        foreach (var r in held)
        {
            if (r.Sev >= floor || (keepAll && r.Sev >= s.BufferLevel))
            {
                r.UserId ??= user;
                Emit(r);
            }
        }
    }

    /// <summary>
    /// Queues a line, within the per-minute cap. Errors and fatals pass the cap up to ten times
    /// over, so a flood of info lines never hides them.
    /// </summary>
    private void Emit(LogRecord r)
    {
        var rate = Settings.Rate;
        if (rate > 0)
        {
            var now = _clock();
            var minute = now.Ticks / TimeSpan.TicksPerMinute;
            bool over;
            LogRecord? report = null;
            lock (_rateLock)
            {
                if (minute != _rateMinute)
                {
                    if (_rateDrop > 0)
                    {
                        report = new LogRecord
                        {
                            At = now,
                            Sev = Severity.Warn,
                            Logger = "visionary",
                            Message = $"visionary: {_rateDrop} log lines dropped over the cap of {rate} a minute (Settings → Logs)",
                        };
                    }
                    _rateMinute = minute;
                    _rateCount = 0;
                    _rateDrop = 0;
                }
                _rateCount++;
                over = _rateCount > rate && (r.Sev < Severity.Error || _rateCount > 10 * rate);
                if (over)
                {
                    _rateDrop++;
                }
            }
            if (report is not null)
            {
                Enqueue(report);
            }
            if (over)
            {
                return;
            }
        }
        Enqueue(r);
    }

    private void Enqueue(LogRecord r)
    {
        if (!Queue.Writer.TryWrite(r))
        {
            Interlocked.Increment(ref _dropped);
        }
    }

    /// <summary>The user set on the request so far; the request is still running, on this flow or one it started.</summary>
    private static string? CurrentUser(RequestLogs req)
    {
        if (req.Context is not { } ctx)
        {
            return null;
        }
        try
        {
            return VisionaryMiddleware.UserOf(ctx)?.Id;
        }
        catch (Exception)
        {
            return null;
        }
    }
}

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Channels;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

// Lines below the level are held per request and written only if the request fails or is slow.
// Nothing on the request path waits for the database.

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

/// <summary>Kept for every request, logged or not, so its lines can be held until it ends.</summary>
internal sealed class RequestLogs
{
    private static readonly AsyncLocal<RequestLogs?> CurrentLogs = new();

    private int _failed;

    public RequestLogs(HttpContext? context, string? traceId)
    {
        Context = context;
        TraceId = traceId;
    }

    /// <summary>Still set after the request ends, for work it left running.</summary>
    public static RequestLogs? Current => CurrentLogs.Value;

    public HttpContext? Context { get; }

    public string? RequestId { get; set; }

    public string? TraceId { get; }

    /// <summary>Guards the fields below.</summary>
    public object Lock { get; } = new();

    public Queue<LogRecord>? Held { get; set; }
    public bool Done { get; set; }
    public string? Route { get; set; }
    public string? UserId { get; set; }

    public bool Failed => Volatile.Read(ref _failed) == 1;

    public void MarkFailed() => Volatile.Write(ref _failed, 1);

    public void Enter() => CurrentLogs.Value = this;
}

internal sealed class AppLogs
{
    public const int QueueSize = 10_000;

    // Per request; past this the oldest held lines are dropped.
    public const int MaxHeld = 500;

    private readonly VisionaryAgent _agent;
    private Func<DateTime> _clock;
    private LogSettings _settings;
    private long _dropped;

    // Per-minute cap; drops are reported once the minute ends.
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

    // VISIONARY_LOG_LEVEL, used until the dashboard settings are read.
    public int BaseLevel { get; }

    public HashSet<string> Secrets { get; }

    public Channel<LogRecord> Queue { get; }

    public LogSettings Settings
    {
        get => Volatile.Read(ref _settings);
        set => Volatile.Write(ref _settings, value);
    }

    internal Func<DateTime> Clock
    {
        set => _clock = value;
    }

    public bool IsEnabled(int sev) => sev > 0 && sev >= Settings.Lowest(_agent.Service, _clock());

    public long TakeDropped() => Interlocked.Exchange(ref _dropped, 0);

    public void Capture(LogRecord r)
    {
        var s = Settings;
        // Skip the SDK's own lines, or a failed write would queue a line behind itself.
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
        // Logged after the request ended, so its route and user are final.
        r.UserId ??= user;
        if (r.Sev >= s.Floor(service, route, user, r.At))
        {
            Emit(r);
        }
    }

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

    // Errors and fatals get ten times the per-minute cap so an info flood can't hide them.
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

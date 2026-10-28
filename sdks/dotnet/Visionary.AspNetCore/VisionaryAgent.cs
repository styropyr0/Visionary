using System;
using System.Net;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Threading;
using System.Threading.Channels;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

namespace Styropyr0.Visionary;

/// <summary>Throughput the middleware counts for the system sampler, even when request logging is off.</summary>
internal sealed class HttpCounters
{
    public long Requests;
    public long Errors;
    public long InFlight;

    public void Observe(int status)
    {
        Interlocked.Increment(ref Requests);
        if (status >= 500)
        {
            Interlocked.Increment(ref Errors);
        }
    }
}

/// <summary>
/// The one per-process collector: where rows go, the bounded write queue, and the span and
/// error intake every hook feeds. With no database configured it is disabled and every entry
/// point is a no-op.
/// </summary>
internal sealed class VisionaryAgent : IDisposable
{
    public const string Sdk = "dotnet";
    public const int QueueSize = 5000;

    private static VisionaryAgent? _current;
    private readonly ErrorLimiter _limiter = new();
    private long _dropped;

    public VisionaryAgent(IOptions<VisionaryOptions> options, ILoggerFactory loggers, IHostApplicationLifetime? lifetime = null)
    {
        Stopping = lifetime?.ApplicationStopping ?? CancellationToken.None;
        Options = options.Value;
        Logger = loggers.CreateLogger("Visionary");
        Instance = string.IsNullOrWhiteSpace(Options.Instance) ? HostName() : Options.Instance.Trim();
        Service = string.IsNullOrWhiteSpace(Options.Service) ? null : Options.Service.Trim();
        Version = Options.Version();
        Runtime = RuntimeInformation.FrameworkDescription;
        SdkVersion = typeof(VisionaryAgent).Assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>()
            ?.InformationalVersion.Split('+')[0] ?? "0.0.0";
        Redactor = new Redactor(Options.RedactHeaders, Options.RedactFields, Options.RedactRequestFields);
        Queue = Channel.CreateBounded<WriteItem>(new BoundedChannelOptions(QueueSize)
        {
            FullMode = BoundedChannelFullMode.Wait,
            SingleReader = true,
        });
        try
        {
            Store = StoreFactory.Create(Options);
            if (Store is null)
            {
                Logger.LogWarning("visionary: no database configured (set VISIONARY_DATABASE_URL, the D1_* variables, or VisionaryOptions.DataSource); collection is off");
            }
        }
        catch (Exception e)
        {
            Logger.LogError("visionary: {Error}; collection is off", e.Message);
        }
        if (Store is not null)
        {
            if (Options.Logs && Options.LogsAdded)
            {
                Logs = new AppLogs(this);
            }
            Interlocked.Exchange(ref _current, this);
        }
    }

    /// <summary>The running agent, for the static API; null when collection is off.</summary>
    public static VisionaryAgent? Current => Volatile.Read(ref _current);

    /// <summary>Cancelled when the application stops; handed to <see cref="Visionary.Background"/> work.</summary>
    public CancellationToken Stopping { get; }

    public VisionaryOptions Options { get; }
    public ILogger Logger { get; }
    public IStore? Store { get; }
    public bool Enabled => Store is not null;
    public string Instance { get; }
    public string? Service { get; }
    public string? Version { get; }
    public string Runtime { get; }
    public string SdkVersion { get; }
    public Redactor Redactor { get; }
    public HttpCounters Http { get; } = new();
    public RequestTally Tally { get; } = new();
    public Channel<WriteItem> Queue { get; }

    /// <summary>Null unless builder.Logging.AddVisionary() was called and VISIONARY_LOGS isn't false.</summary>
    public AppLogs? Logs { get; }

    public bool SpansOn => Enabled && Options.Spans;
    public bool ErrorsOn => Enabled && Options.Errors;

    /// <summary>Queues a row without blocking; when the queue is full it is dropped and counted.</summary>
    public void Enqueue(WriteItem item)
    {
        if (!Queue.Writer.TryWrite(item))
        {
            Interlocked.Increment(ref _dropped);
        }
    }

    public long TakeDropped() => Interlocked.Exchange(ref _dropped, 0);

    /// <summary>
    /// Records a finished call. Inside a request it counts toward the request's totals (and its
    /// rows, while under the cap); after its request was logged (work the request started in the
    /// background) it keeps the request's id and offset and is stored up to the cap of such late
    /// calls, then only when slow; outside any request it is stored only when it is slow itself.
    /// </summary>
    public void RecordSpan(string kind, string name, string? target, string? detail, DateTime startedAt,
        double durationMs, string? error, long? rowCount)
    {
        if (!SpansOn || Suppress.Active)
        {
            return;
        }
        var span = new SpanRecord
        {
            StartedAt = startedAt,
            Kind = kind,
            Name = Cap(name, 255) ?? kind,
            Target = Cap(target, 255),
            Detail = Cap(detail, SqlText.MaxDetail),
            DurationMs = Math.Round(Math.Max(0, durationMs), 3),
            Error = Cap(error, 500),
            RowCount = rowCount,
        };
        var state = RequestState.Tracked;
        if (state is not null)
        {
            span = span with
            {
                RequestId = state.RequestId,
                OffsetMs = Math.Round(Math.Max(0, (startedAt - state.StartedAt).TotalMilliseconds), 3),
            };
            if (state.Add(span))
            {
                return;
            }
            // Work the request started and did not wait for: it keeps the request's id and
            // offsets but stays out of the totals, which were already written.
            if (state.KeepLate(span.DurationMs, Options.SpanThresholdMs))
            {
                Enqueue(span);
            }
            return;
        }
        if (span.DurationMs >= Options.SpanThresholdMs)
        {
            Enqueue(span);
        }
    }

    /// <summary>
    /// Records an exception: handled ones come from <see cref="Visionary.CaptureException(Exception)"/>,
    /// unhandled ones from the middleware. Linked to the request when there is one.
    /// </summary>
    public void CaptureError(Exception ex, bool handled, HttpContext? context)
    {
        // An error keeps the debug lines the request held back.
        if (RequestLogs.Current is { } logs && (context is null || ReferenceEquals(logs.Context, context)))
        {
            logs.MarkFailed();
        }
        if (!ErrorsOn || !ErrorReport.MarkSeen(ex))
        {
            return;
        }
        try
        {
            var now = DateTime.UtcNow;
            var fingerprint = ErrorReport.Fingerprint(ex);
            if (!_limiter.Allow(fingerprint, now))
            {
                return;
            }
            var state = RequestState.Current;
            context ??= state?.Context;
            if (state is not null && !ReferenceEquals(state.Context, context))
            {
                state = null;
            }
            Enqueue(new ErrorRecord
            {
                CreatedAt = now,
                RequestId = state?.RequestId,
                Method = context?.Request.Method,
                Route = context is null ? null : VisionaryMiddleware.RouteOf(context, Options),
                Path = context is null ? null : (context.Request.PathBase + context.Request.Path).Value,
                UserId = context is null ? null : VisionaryMiddleware.UserOf(context)?.Id,
                Type = ErrorReport.Type(ex),
                Message = ErrorReport.Message(ex),
                Stack = ErrorReport.Stack(ex),
                Fingerprint = fingerprint,
                Handled = handled,
            });
        }
        catch (Exception e)
        {
            // Never let error reporting become the application's error.
            Logger.LogDebug(e, "visionary: capturing an exception failed");
        }
    }

    public void Dispose()
    {
        Interlocked.CompareExchange(ref _current, null, this);
        (Store as IDisposable)?.Dispose();
    }

    internal static string HostName()
    {
        try
        {
            return Dns.GetHostName();
        }
        catch (Exception)
        {
            return Environment.MachineName;
        }
    }

    private static string? Cap(string? s, int max) =>
        string.IsNullOrEmpty(s) ? null : s.Length > max ? s[..max] : s;
}

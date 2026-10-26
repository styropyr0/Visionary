using System;
using System.Diagnostics;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

/// <summary>
/// What application code calls: attach the user, name the route, report a handled exception,
/// or time a custom operation. Every method is a no-op when collection is off.
/// </summary>
public static class Visionary
{
    /// <summary>
    /// Attaches the signed-in user to the request being logged; call it from auth code once the
    /// user is known. Without it, the authenticated <c>HttpContext.User</c> is used.
    /// </summary>
    public static void SetUser(HttpContext context, string? id, string? handle = null, string? role = null)
    {
        ArgumentNullException.ThrowIfNull(context);
        if (string.IsNullOrEmpty(id))
        {
            context.Items.Remove(VisionaryMiddleware.UserKey);
            return;
        }
        context.Items[VisionaryMiddleware.UserKey] = new VisionaryUser(id, handle, role);
    }

    /// <summary>
    /// Records the account a sign-in request tried: the username or email typed, whether or not
    /// it exists or the password was right. Call it in the login handler; the security detectors
    /// use it to tell one IP trying many accounts, many IPs trying one account, and a signed-in
    /// user trying someone else's.
    /// </summary>
    public static void LoginAttempt(HttpContext context, string? account)
    {
        ArgumentNullException.ThrowIfNull(context);
        if (account is null)
        {
            context.Items.Remove(VisionaryMiddleware.LoginKey);
            return;
        }
        context.Items[VisionaryMiddleware.LoginKey] = account;
    }

    /// <summary>Records the route pattern (e.g. /users/{id}) for requests endpoint routing doesn't see.</summary>
    public static void SetRoute(HttpContext context, string pattern)
    {
        ArgumentNullException.ThrowIfNull(context);
        context.Items[VisionaryMiddleware.RouteKey] = pattern;
    }

    /// <summary>Records a handled exception (handled = 1), linked to the current request when there is one.</summary>
    public static void CaptureException(Exception exception)
    {
        ArgumentNullException.ThrowIfNull(exception);
        VisionaryAgent.Current?.CaptureError(exception, handled: true, context: null);
    }

    /// <summary>Records a handled exception for a specific request.</summary>
    public static void CaptureException(HttpContext context, Exception exception)
    {
        ArgumentNullException.ThrowIfNull(exception);
        VisionaryAgent.Current?.CaptureError(exception, handled: true, context);
    }

    /// <summary>
    /// Times an operation the SDK can't see by itself (a cache or queue call, a database without
    /// diagnostics); dispose the span when it ends.
    /// </summary>
    /// <param name="kind">db, http, cache, queue, custom, graphql or background.</param>
    /// <param name="name">Short and low-cardinality, used for grouping, e.g. "GET session".</param>
    /// <param name="target">The system called, e.g. "redis/cache-1".</param>
    /// <param name="detail">More detail, e.g. the command; never parameter values or secrets.</param>
    public static VisionarySpan StartSpan(string kind, string name, string? target = null, string? detail = null) =>
        new(kind, name, target, detail);

    /// <summary>
    /// Starts work the request does not wait for (sending an email after the response, say) on
    /// the thread pool and records it as one span of kind <c>background</c>, named
    /// <paramref name="name"/>, failed with the exception's message if the work throws. The work
    /// keeps the request's tracking, so its calls carry the request's id and offsets even after
    /// the response was sent, but not the request's cancellation: <c>RequestAborted</c> does not
    /// stop it. The token passed in is cancelled when the application stops. The returned task
    /// completes (or faults) with the work. With collection off the work still runs.
    /// </summary>
    /// <param name="name">Short and low-cardinality, e.g. "send welcome email".</param>
    /// <param name="work">The work; observe the token to stop on shutdown.</param>
    public static Task Background(string name, Func<CancellationToken, Task> work)
    {
        ArgumentNullException.ThrowIfNull(work);
        var token = VisionaryAgent.Current?.Stopping ?? CancellationToken.None;
        return Task.Run(async () =>
        {
            var startedAt = DateTime.UtcNow;
            var started = Stopwatch.GetTimestamp();
            string? error = null;
            try
            {
                await work(token).ConfigureAwait(false);
            }
            catch (Exception e)
            {
                error = string.IsNullOrEmpty(e.Message) ? e.GetType().Name : e.Message;
                throw;
            }
            finally
            {
                VisionaryAgent.Current?.RecordSpan("background", name, null, null, startedAt,
                    Stopwatch.GetElapsedTime(started).TotalMilliseconds, error, null);
            }
        }, CancellationToken.None);
    }
}

/// <summary>A custom span in progress; see <see cref="Visionary.StartSpan"/>.</summary>
public sealed class VisionarySpan : IDisposable
{
    private readonly string _kind;
    private readonly string _name;
    private readonly string? _target;
    private readonly string? _detail;
    private readonly DateTime _startedAt = DateTime.UtcNow;
    private readonly long _started = Stopwatch.GetTimestamp();
    private string? _error;
    private bool _ended;

    internal VisionarySpan(string kind, string name, string? target, string? detail)
    {
        _kind = kind;
        _name = name;
        _target = target;
        _detail = detail;
    }

    /// <summary>Rows returned or affected, when known.</summary>
    public long? RowCount { get; set; }

    /// <summary>Marks the span as failed.</summary>
    public void SetError(string message) => _error = string.IsNullOrEmpty(message) ? "error" : message;

    /// <summary>Marks the span as failed with the exception's message.</summary>
    public void SetError(Exception exception) => SetError(exception.Message);

    /// <summary>Ends the span and records it.</summary>
    public void Dispose()
    {
        if (_ended)
        {
            return;
        }
        _ended = true;
        VisionaryAgent.Current?.RecordSpan(_kind, _name, _target, _detail, _startedAt,
            Stopwatch.GetElapsedTime(_started).TotalMilliseconds, _error, RowCount);
    }
}

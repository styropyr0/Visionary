using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Runtime.ExceptionServices;
using System.Security.Claims;
using System.Security.Cryptography;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Diagnostics;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Http.Features;
using Microsoft.AspNetCore.Routing;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Logs each request, counts throughput and records unhandled exceptions. Mounted first in the
/// pipeline so failures are logged with the status the client got. Capture is cheap; redaction
/// and storage happen on the background writer.
/// </summary>
internal sealed class VisionaryMiddleware
{
    public const string RequestIdHeader = "X-Request-ID";
    internal const string UserKey = "__visionary.user";
    internal const string RouteKey = "__visionary.route";

    private readonly RequestDelegate _next;
    private readonly VisionaryAgent _agent;

    public VisionaryMiddleware(RequestDelegate next, VisionaryAgent agent)
    {
        _next = next;
        _agent = agent;
    }

    public async Task InvokeAsync(HttpContext ctx)
    {
        var o = _agent.Options;
        var counters = _agent.Http;
        var tally = _agent.Tally;
        Interlocked.Increment(ref counters.InFlight);
        tally.Start();
        var started = Stopwatch.GetTimestamp();
        var path = (ctx.Request.PathBase + ctx.Request.Path).Value ?? "/";
        try
        {
            if (!o.LogRequests || !path.StartsWith(o.PathPrefix, StringComparison.Ordinal))
            {
                await PassThrough(ctx).ConfigureAwait(false);
                return;
            }
            await Logged(ctx, path).ConfigureAwait(false);
        }
        finally
        {
            Interlocked.Decrement(ref counters.InFlight);
            if (tally.Collecting)
            {
                // Only while a CPU profile runs: the time each route's requests took.
                try
                {
                    tally.Finish(ctx.Request.Method, RouteOf(ctx, o), path, Stopwatch.GetElapsedTime(started).TotalMilliseconds);
                }
                catch (Exception e)
                {
                    _agent.Logger.LogDebug(e, "visionary: request tally failed");
                }
            }
        }
    }

    private async Task PassThrough(HttpContext ctx)
    {
        try
        {
            await _next(ctx).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _agent.Http.Observe(500);
            Unhandled(ex, ctx);
            throw;
        }
        _agent.Http.Observe(ctx.Response.StatusCode);
        HandledByFramework(ctx);
    }

    private async Task Logged(HttpContext ctx, string path)
    {
        var o = _agent.Options;
        var startedAt = DateTime.UtcNow;
        var started = Stopwatch.GetTimestamp();

        var requestId = ctx.Request.Headers[RequestIdHeader].ToString();
        if (requestId.Length == 0 || requestId.Length > 128)
        {
            requestId = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
        }
        ctx.Response.Headers[RequestIdHeader] = requestId;

        var state = new RequestState(ctx, requestId, startedAt, o.SpanMax);
        state.Enter();

        var headers = new List<KeyValuePair<string, string>>(ctx.Request.Headers.Count);
        foreach (var (name, values) in ctx.Request.Headers)
        {
            headers.Add(new(name, string.Join(", ", values.Where(v => v is not null))));
        }
        var reqType = ctx.Request.ContentType;
        var maxBody = Math.Max(0, o.MaxBodyBytes);

        RequestCapture? reqCapture = null;
        var originalRequestBody = ctx.Request.Body;
        if (o.LogBodies && ctx.Features.Get<IHttpRequestBodyDetectionFeature>()?.CanHaveBody != false && Capturable.Check(reqType))
        {
            reqCapture = new RequestCapture(originalRequestBody, maxBody);
            ctx.Request.Body = reqCapture;
        }
        var originalResponseBody = ctx.Features.Get<IHttpResponseBodyFeature>();
        ResponseCapture? resCapture = null;
        if (originalResponseBody is not null)
        {
            resCapture = new ResponseCapture(originalResponseBody, ctx.Response, o.LogBodies, maxBody);
            ctx.Features.Set<IHttpResponseBodyFeature>(resCapture);
        }

        Exception? error = null;
        try
        {
            await _next(ctx).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            error = ex;
        }

        try
        {
            if (resCapture is not null)
            {
                await resCapture.FinishAsync().ConfigureAwait(false);
            }
        }
        catch (Exception)
        {
            // the client went away; the status and sizes are still worth logging
        }
        finally
        {
            if (resCapture is not null)
            {
                ctx.Features.Set(resCapture.Inner);
            }
            if (reqCapture is not null)
            {
                ctx.Request.Body = originalRequestBody;
            }
        }

        var status = error is not null && !ctx.Response.HasStarted ? StatusCodes.Status500InternalServerError : ctx.Response.StatusCode;
        _agent.Http.Observe(status);
        try
        {
            if (error is not null)
            {
                Unhandled(error, ctx);
            }
            else
            {
                HandledByFramework(ctx);
            }
            var (totals, spans) = state.Close();
            var durationMs = Stopwatch.GetElapsedTime(started).TotalMilliseconds;
            var responseHeaders = ctx.Response.Headers;
            _agent.Enqueue(new RequestEntry
            {
                RequestId = requestId,
                StartedAt = startedAt,
                Method = ctx.Request.Method,
                Path = path,
                Route = RouteOf(ctx, o),
                Query = ctx.Request.QueryString.HasValue ? ctx.Request.QueryString.Value![1..] : null,
                Status = status,
                DurationMs = Math.Round(durationMs, 3),
                Ip = ClientIp(ctx),
                UserAgent = ctx.Request.Headers.UserAgent.ToString(),
                User = UserOf(ctx),
                RequestHeaders = headers,
                RequestContentType = reqType,
                RequestBody = reqCapture?.Text(),
                RequestTruncated = reqCapture?.Truncated ?? false,
                RequestSize = Math.Max(ctx.Request.ContentLength ?? 0, reqCapture?.Size ?? 0),
                ResponseContentType = ctx.Response.ContentType,
                ResponseBody = resCapture?.Text(),
                ResponseTruncated = resCapture?.Truncated ?? false,
                ResponseSize = Math.Max(ctx.Response.ContentLength ?? 0, resCapture?.Written ?? 0),
                Totals = _agent.SpansOn ? totals : null,
                Spans = _agent.SpansOn ? spans : Array.Empty<SpanRecord>(),
            });
        }
        catch (Exception e)
        {
            _agent.Logger.LogDebug(e, "visionary: request capture failed");
        }

        if (error is not null)
        {
            ExceptionDispatchInfo.Capture(error).Throw();
        }
    }

    /// <summary>An exception that escaped the pipeline; the framework will answer 500.</summary>
    private void Unhandled(Exception ex, HttpContext ctx)
    {
        // A client that hung up cancels the request; that is not an application error.
        if (ex is OperationCanceledException && ctx.RequestAborted.IsCancellationRequested)
        {
            return;
        }
        _agent.CaptureError(ex, handled: false, ctx);
    }

    /// <summary>
    /// UseExceptionHandler placed after this middleware turns exceptions into responses before
    /// they reach it; it leaves the exception in a feature, which still counts as unhandled.
    /// </summary>
    private void HandledByFramework(HttpContext ctx)
    {
        if (ctx.Features.Get<IExceptionHandlerFeature>()?.Error is { } ex)
        {
            _agent.CaptureError(ex, handled: false, ctx);
        }
    }

    /// <summary>
    /// The matched route pattern: an explicit <see cref="Visionary.SetRoute"/>, the options hook,
    /// then endpoint routing (also after UseExceptionHandler cleared the endpoint).
    /// </summary>
    internal static string? RouteOf(HttpContext ctx, VisionaryOptions o)
    {
        if (ctx.Items.TryGetValue(RouteKey, out var r) && r is string set && set.Length > 0)
        {
            return set;
        }
        var custom = o.Route?.Invoke(ctx);
        if (!string.IsNullOrEmpty(custom))
        {
            return custom;
        }
        var endpoint = ctx.GetEndpoint() ?? ctx.Features.Get<IExceptionHandlerPathFeature>()?.Endpoint;
        var raw = (endpoint as RouteEndpoint)?.RoutePattern.RawText;
        if (string.IsNullOrEmpty(raw))
        {
            return null;
        }
        // Inline constraints and defaults don't change the route: /users/{id:int} is /users/{id},
        // as other SDKs write it. Controllers' attribute routes come without the leading slash.
        raw = RouteConstraints.Replace(raw, "{$1$2}");
        return raw.StartsWith('/') ? raw : "/" + raw;
    }

    private static readonly Regex RouteConstraints = new(@"\{(\*{0,2}[^{}:=?]+)(?::[^{}=?]+)*(?:=[^{}?]*)?(\??)\}",
        RegexOptions.Compiled | RegexOptions.CultureInvariant);

    /// <summary>The user set with <see cref="Visionary.SetUser"/>, else the authenticated principal.</summary>
    internal static VisionaryUser? UserOf(HttpContext ctx)
    {
        if (ctx.Items.TryGetValue(UserKey, out var u) && u is VisionaryUser set)
        {
            return set;
        }
        var principal = ctx.User;
        if (principal?.Identity?.IsAuthenticated != true)
        {
            return null;
        }
        var id = principal.FindFirst(ClaimTypes.NameIdentifier)?.Value ?? principal.FindFirst("sub")?.Value ?? principal.Identity.Name;
        if (string.IsNullOrEmpty(id))
        {
            return null;
        }
        var handle = principal.Identity.Name ?? principal.FindFirst("preferred_username")?.Value ?? principal.FindFirst(ClaimTypes.Email)?.Value;
        var roleType = (principal.Identity as ClaimsIdentity)?.RoleClaimType ?? ClaimTypes.Role;
        var role = principal.FindFirst(roleType)?.Value ?? principal.FindFirst("role")?.Value;
        return new VisionaryUser(id, handle, role);
    }

    /// <summary>
    /// The first X-Forwarded-For hop, as most deployments sit behind a proxy; informational only
    /// and never used for decisions.
    /// </summary>
    private static string? ClientIp(HttpContext ctx)
    {
        var fwd = ctx.Request.Headers["X-Forwarded-For"].ToString();
        if (fwd.Length > 0)
        {
            return fwd.Split(',')[0].Trim();
        }
        var ip = ctx.Connection.RemoteIpAddress;
        if (ip is null)
        {
            return null;
        }
        return (ip.IsIPv4MappedToIPv6 ? ip.MapToIPv4() : ip).ToString();
    }
}

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary;

/// <summary>Turns exceptions into errors rows: type, message, stack and a stable fingerprint.</summary>
internal static class ErrorReport
{
    public const int MaxMessage = 1000;
    public const int MaxStack = 16 * 1024;
    private const int FingerprintFrames = 5;

    private static readonly char[] PathSeparators = { '/', '\\' };

    private static readonly Regex Digits = new(@"\d+", RegexOptions.Compiled);

    // Exceptions already recorded, so one rethrown through several layers is stored once.
    private static readonly ConditionalWeakTable<Exception, object> Seen = new();

    /// <summary>Marks an exception as recorded; false when it already was.</summary>
    public static bool MarkSeen(Exception ex)
    {
        lock (Seen)
        {
            if (Seen.TryGetValue(ex, out _))
            {
                return false;
            }
            Seen.Add(ex, true);
            return true;
        }
    }

    public static string Type(Exception ex) => ex.GetType().FullName ?? ex.GetType().Name;

    public static string? Message(Exception ex) => Cap(ex.Message, MaxMessage);

    public static string? Stack(Exception ex) => Cap(ex.ToString(), MaxStack);

    /// <summary>
    /// The first 16 hex characters of SHA-1 over type + "\n" + frames, where frames are the top
    /// five frames of the application's own code as function@file, without line numbers. With no
    /// frames, type + "\n" + message with every digit run replaced by 0.
    /// </summary>
    public static string Fingerprint(Exception ex)
    {
        var type = Type(ex);
        var frames = Frames(ex);
        var app = frames.Where(f => f.InApp).Take(FingerprintFrames).ToList();
        if (app.Count == 0)
        {
            app = frames.Take(FingerprintFrames).ToList();
        }
        var basis = app.Count > 0
            ? type + "\n" + string.Join("\n", app.Select(f => f.Text))
            : type + "\n" + Digits.Replace(ex.Message, "0");
        var hash = SHA1.HashData(Encoding.UTF8.GetBytes(basis));
        return Convert.ToHexString(hash).ToLowerInvariant()[..16];
    }

    private readonly record struct Frame(string Text, bool InApp);

    private static List<Frame> Frames(Exception ex)
    {
        var list = new List<Frame>();
        StackFrame[] frames;
        try
        {
            // Innermost first: where the exception was thrown, then its callers.
            frames = new StackTrace(ex, fNeedFileInfo: true).GetFrames();
        }
        catch (Exception)
        {
            return list;
        }
        foreach (var f in frames)
        {
            var method = f.GetMethod();
            if (method is null)
            {
                continue;
            }
            var (type, name) = Readable(method);
            var file = f.GetFileName();
            // The PDB keeps the build machine's path; cut at either separator so an app built on
            // Windows and run on Linux fingerprints the same.
            file = string.IsNullOrEmpty(file) ? method.Module.Name : file[(file.LastIndexOfAny(PathSeparators) + 1)..];
            list.Add(new Frame($"{type?.FullName?.Replace('+', '.') ?? "?"}.{name}@{file}", InApp(type)));
        }
        return list;
    }

    /// <summary>
    /// The type and method a developer wrote: async state machines (Type+&lt;Run&gt;d__3.MoveNext)
    /// and lambda closures report the method that contains them.
    /// </summary>
    private static (Type? Type, string Name) Readable(MethodBase method)
    {
        var type = method.DeclaringType;
        var name = method.Name;
        while (type is not null && type.IsNested && type.Name.StartsWith('<'))
        {
            if (name is "MoveNext" or "SetStateMachine")
            {
                var close = type.Name.IndexOf('>');
                if (close > 1)
                {
                    name = type.Name[1..close];
                }
            }
            type = type.DeclaringType;
        }
        return (type, name);
    }

    /// <summary>Application code: not the framework, the runtime or this SDK.</summary>
    private static bool InApp(Type? type)
    {
        var ns = type?.Namespace ?? "";
        static bool Under(string ns, string root) => ns == root || ns.StartsWith(root + ".", StringComparison.Ordinal);
        return type is not null &&
               !Under(ns, "System") && !Under(ns, "Microsoft") && !Under(ns, "Visionary") &&
               !Under(ns, "Styropyr0.Visionary");
    }

    private static string? Cap(string? s, int max) =>
        string.IsNullOrEmpty(s) ? null : s.Length > max ? s[..max] : s;
}

/// <summary>At most 20 rows per fingerprint per minute per process; the rest are dropped.</summary>
internal sealed class ErrorLimiter
{
    private const int PerMinute = 20;
    private readonly Dictionary<string, (long Minute, int Count)> _counts = new();

    public bool Allow(string fingerprint, DateTime now)
    {
        var minute = now.Ticks / TimeSpan.TicksPerMinute;
        lock (_counts)
        {
            if (_counts.Count > 10_000)
            {
                foreach (var stale in _counts.Where(kv => kv.Value.Minute != minute).Select(kv => kv.Key).ToList())
                {
                    _counts.Remove(stale);
                }
            }
            var c = _counts.TryGetValue(fingerprint, out var v) && v.Minute == minute ? v.Count : 0;
            if (c >= PerMinute)
            {
                return false;
            }
            _counts[fingerprint] = (minute, c + 1);
            return true;
        }
    }
}

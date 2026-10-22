using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Diagnostics.Tracing;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Diagnostics.NETCore.Client;
using Microsoft.Diagnostics.Symbols;
using Microsoft.Diagnostics.Tracing;
using Microsoft.Diagnostics.Tracing.Etlx;
using Microsoft.Diagnostics.Tracing.Parsers;

namespace Styropyr0.Visionary.Profiling;

/// <summary>A finished sampling window: the trace file it wrote and how long it ran.</summary>
internal sealed record Recording(string TracePath, DateTime StartedAt, double DurationMs);

/// <summary>Folded stacks from one recording.</summary>
internal sealed record FoldedProfile(string Folded, long Samples);

/// <summary>
/// Samples this process through EventPipe, the runtime's own tracing channel, by opening a
/// session on the process's diagnostics port (what dotnet-trace does from outside). The
/// runtime's sample profiler walks every managed thread's stack about once a millisecond;
/// the session is written to a temporary .nettrace file, then read back with TraceEvent, whose
/// method rundown at the end of the session names every frame.
/// </summary>
internal static class EventPipeSampler
{
    private const string SampleProvider = "Microsoft-DotNETCore-SampleProfiler";

    /// <summary>Records <paramref name="duration"/> of samples into a temporary file.</summary>
    public static async Task<Recording> RecordAsync(TimeSpan duration, CancellationToken ct)
    {
        var providers = new[]
        {
            new EventPipeProvider(SampleProvider, EventLevel.Informational),
            // Method and module loads for code compiled while sampling; rundown covers the rest.
            new EventPipeProvider("Microsoft-Windows-DotNETRuntime", EventLevel.Informational,
                (long)(ClrTraceEventParser.Keywords.Loader | ClrTraceEventParser.Keywords.Jit |
                       ClrTraceEventParser.Keywords.JittedMethodILToNativeMap)),
        };
        var path = Path.Combine(Path.GetTempPath(), $"visionary-profile-{Environment.ProcessId}-{Guid.NewGuid():N}.nettrace");
        var client = new DiagnosticsClient(Environment.ProcessId);
        var startedAt = DateTime.UtcNow;
        var clock = Stopwatch.StartNew();
        double durationMs;
        try
        {
            using var session = await client.StartEventPipeSessionAsync(providers, requestRundown: true, circularBufferMB: 64, ct)
                .ConfigureAwait(false);
            await using (var file = new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.None, 1 << 16, FileOptions.Asynchronous))
            {
                var copy = session.EventStream.CopyToAsync(file, CancellationToken.None);
                try
                {
                    await Task.Delay(duration, ct).ConfigureAwait(false);
                }
                finally
                {
                    durationMs = clock.Elapsed.TotalMilliseconds;
                    // Stop asks for the rundown, then ends the stream.
                    await session.StopAsync(CancellationToken.None).ConfigureAwait(false);
                    await copy.ConfigureAwait(false);
                }
            }
        }
        catch
        {
            Delete(path);
            throw;
        }
        return new Recording(path, startedAt, durationMs);
    }

    /// <summary>
    /// Reads a recording back as folded stacks. Only threads running managed code count, and
    /// not those parked in a wait or doing the SDK's own background work.
    /// </summary>
    public static FoldedProfile Fold(Recording rec, FrameNamer namer)
    {
        var etlx = Path.ChangeExtension(rec.TracePath, ".etlx");
        try
        {
            TraceLog.CreateFromEventPipeDataFile(rec.TracePath, etlx);
            using var log = new TraceLog(etlx);
            using var symbols = new SymbolReader(TextWriter.Null, AppContext.BaseDirectory) { SecurityCheck = _ => true };

            // Samples per stack first: stacks repeat, so each is named once.
            var perStack = new Dictionary<CallStackIndex, long>();
            foreach (var e in log.Events)
            {
                if (e.ProviderName != SampleProvider || !IsManaged(e))
                {
                    continue;
                }
                var idx = e.CallStackIndex();
                if (idx != CallStackIndex.Invalid)
                {
                    perStack[idx] = perStack.TryGetValue(idx, out var n) ? n + 1 : 1;
                }
            }

            var names = new Dictionary<CodeAddressIndex, (string Name, string Method, string? Module)>();
            var lines = new Dictionary<string, long>(StringComparer.Ordinal);
            var frames = new List<string>(64);
            long samples = 0;
            foreach (var (idx, count) in perStack)
            {
                frames.Clear();
                var skip = false;
                var lastMethod = "";
                for (var st = log.CallStacks[idx]; st is not null; st = st.Caller)
                {
                    var ca = st.CodeAddress;
                    if (!names.TryGetValue(ca.CodeAddressIndex, out var f))
                    {
                        f = Describe(ca, namer, symbols);
                        names[ca.CodeAddressIndex] = f;
                    }
                    if (frames.Count == 0 && SampleFilter.IsWaitLeaf(f.Method))
                    {
                        skip = true;
                        break;
                    }
                    if (SampleFilter.IsSdkFrame(f.Method, f.Module))
                    {
                        skip = true;
                        break;
                    }
                    // Runs of unnamed native frames read as one, and so does an async method's
                    // stub calling its own state machine (both are named after the method).
                    if (frames.Count > 0 && f.Method == lastMethod)
                    {
                        if (HasLine(f.Name) && !HasLine(frames[^1]))
                        {
                            frames[^1] = f.Name;
                        }
                        continue;
                    }
                    lastMethod = f.Method;
                    frames.Add(f.Name);
                }
                if (skip || frames.Count == 0)
                {
                    continue;
                }
                // Only the sampled frame keeps its line: a caller is one frame whichever line it
                // called from, which keeps stacks few (SPEC.md "CPU profiles").
                for (var i = 1; i < frames.Count; i++)
                {
                    frames[i] = FrameNamer.WithoutLine(frames[i]);
                }
                frames.Reverse();
                var key = string.Join(';', frames);
                lines[key] = lines.TryGetValue(key, out var n) ? n + count : count;
                samples += count;
            }
            return new FoldedProfile(Folder.Fold(lines), samples);
        }
        finally
        {
            Delete(etlx);
            Delete(rec.TracePath);
        }
    }

    private static bool HasLine(string name)
    {
        var colon = name.LastIndexOf(':');
        return colon > 0 && colon < name.Length - 1 && char.IsAsciiDigit(name[^1]) && !name.Contains(" lib:", StringComparison.Ordinal) &&
            !name.Contains(" std:", StringComparison.Ordinal);
    }

    public static void Delete(string path)
    {
        try
        {
            File.Delete(path);
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException)
        {
            // temp files; the OS cleans up what is left
        }
    }

    /// <summary>The sampler's Type field: 2 (Managed) for a thread running managed code, 1 (External) in native code or blocked.</summary>
    private static bool IsManaged(TraceEvent e)
    {
        var v = e.PayloadByName("Type");
        return v switch
        {
            string s => s == "Managed",
            null => false,
            _ => Convert.ToInt64(v, System.Globalization.CultureInfo.InvariantCulture) == 2,
        };
    }

    private static (string Name, string Method, string? Module) Describe(TraceCodeAddress ca, FrameNamer namer, SymbolReader symbols)
    {
        var full = ca.FullMethodName;
        var module = string.IsNullOrEmpty(ca.ModuleName) ? null : ca.ModuleName;
        var path = ca.ModuleFile?.FilePath;
        var method = FrameNamer.CleanMethod(full);
        string? file = null;
        var line = 0;
        // Line numbers only for the app's own code: its PDBs sit next to it, and lookups cost.
        if (module is not null && method.Length > 0 && namer.KindOf(module, path) == CodeKind.App)
        {
            try
            {
                var loc = ca.GetSourceLine(symbols);
                if (loc is not null && loc.LineNumber > 0)
                {
                    file = loc.SourceFile?.BuildTimeFilePath;
                    line = loc.LineNumber;
                }
            }
            catch (Exception)
            {
                // no PDB, or one TraceEvent can't read: the frame keeps its module name
            }
        }
        return (namer.Name(full, module, path, file, line), method, module);
    }
}

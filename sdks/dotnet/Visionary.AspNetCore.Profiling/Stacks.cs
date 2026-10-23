using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary.Profiling;

/// <summary>Whose code a module is, which the flame graph colours by.</summary>
internal enum CodeKind
{
    /// <summary>The application's own assemblies.</summary>
    App,
    /// <summary>A NuGet package.</summary>
    Lib,
    /// <summary>The .NET runtime and shared frameworks (Microsoft.NETCore.App, Microsoft.AspNetCore.App).</summary>
    Std,
}

/// <summary>
/// Sorts assemblies into app, package and framework code. The app's deps.json says which
/// assemblies came from which NuGet package and which from the app's own projects; the shared
/// framework directory and the System.* / Microsoft.* names cover the rest.
/// </summary>
internal sealed class CodeOwners
{
    private readonly Dictionary<string, (CodeKind Kind, string Package)> _deps = new(StringComparer.OrdinalIgnoreCase);
    private readonly string? _entry;
    private readonly string? _sharedDir;
    private readonly string? _appDir;

    public CodeOwners(string? depsJson, string? entryAssembly, string? sharedFrameworkDir, string? appDir)
    {
        _entry = entryAssembly;
        _sharedDir = Dir(sharedFrameworkDir);
        _appDir = Dir(appDir);
        if (!string.IsNullOrEmpty(depsJson))
        {
            try
            {
                ReadDeps(depsJson);
            }
            catch (Exception e) when (e is JsonException or InvalidOperationException or KeyNotFoundException)
            {
                // without deps.json, names and locations decide
            }
        }
    }

    /// <summary>The running app: its deps.json, entry assembly, shared framework and base directory.</summary>
    public static CodeOwners ForCurrentApp()
    {
        string? deps = null;
        try
        {
            // The app's own deps.json comes first; the frameworks' follow.
            var files = (AppContext.GetData("APP_CONTEXT_DEPS_FILES") as string)?.Split(';', StringSplitOptions.RemoveEmptyEntries);
            if (files is { Length: > 0 } && File.Exists(files[0]))
            {
                deps = File.ReadAllText(files[0]);
            }
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException)
        {
            // fall back to names and locations
        }
        // typeof(object) lives in <dotnet>/shared/Microsoft.NETCore.App/<version>/.
        var core = Path.GetDirectoryName(typeof(object).Assembly.Location);
        var shared = core is null ? null : Path.GetDirectoryName(Path.GetDirectoryName(core));
        return new CodeOwners(deps, Assembly.GetEntryAssembly()?.GetName().Name, shared, AppContext.BaseDirectory);
    }

    public (CodeKind Kind, string Package) Of(string module, string? modulePath)
    {
        if (_deps.TryGetValue(module, out var known))
        {
            return known;
        }
        var dir = Dir(Path.GetDirectoryName(modulePath ?? ""));
        if (_sharedDir is not null && dir is not null && dir.StartsWith(_sharedDir, StringComparison.OrdinalIgnoreCase))
        {
            return (CodeKind.Std, module);
        }
        if (string.Equals(module, _entry, StringComparison.OrdinalIgnoreCase))
        {
            return (CodeKind.App, module);
        }
        if (IsFrameworkName(module))
        {
            return (CodeKind.Std, module);
        }
        if (_appDir is not null && dir is not null && string.Equals(dir, _appDir, StringComparison.OrdinalIgnoreCase))
        {
            // Next to the app but not from a package: the app's own (or a copied-in) assembly.
            return (CodeKind.App, module);
        }
        return (CodeKind.Lib, module);
    }

    private static bool IsFrameworkName(string module) =>
        module.StartsWith("System.", StringComparison.Ordinal) || module.StartsWith("Microsoft.", StringComparison.Ordinal) ||
        module is "System" or "mscorlib" or "netstandard" or "WindowsBase";

    private static string? Dir(string? path)
    {
        if (string.IsNullOrEmpty(path))
        {
            return null;
        }
        return path.Replace('\\', '/').TrimEnd('/');
    }

    /// <summary>libraries say each library's type; targets list the assemblies each one ships.</summary>
    private void ReadDeps(string json)
    {
        using var doc = JsonDocument.Parse(json);
        var root = doc.RootElement;
        if (!root.TryGetProperty("libraries", out var libraries) || !root.TryGetProperty("targets", out var targets))
        {
            return;
        }
        var types = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        foreach (var lib in libraries.EnumerateObject())
        {
            types[lib.Name] = lib.Value.TryGetProperty("type", out var t) ? t.GetString() ?? "" : "";
        }
        foreach (var target in targets.EnumerateObject())
        {
            foreach (var lib in target.Value.EnumerateObject())
            {
                if (!lib.Value.TryGetProperty("runtime", out var runtime) || runtime.ValueKind != JsonValueKind.Object)
                {
                    continue;
                }
                var name = lib.Name.Split('/')[0];
                var kind = (types.TryGetValue(lib.Name, out var type) ? type : "") switch
                {
                    "project" => CodeKind.App,
                    "runtimepack" or "referenceassembly" => CodeKind.Std,
                    _ => CodeKind.Lib,
                };
                foreach (var file in runtime.EnumerateObject())
                {
                    var asm = Path.GetFileNameWithoutExtension(file.Name);
                    _deps.TryAdd(asm, (kind, kind == CodeKind.Std ? asm : name));
                }
            }
            break; // the first target is the one the app runs
        }
    }
}

/// <summary>
/// Turns a sampled .NET frame into the "function file:line" form of SPEC.md "CPU profiles".
/// <list type="bullet">
/// <item>The app's code with a PDB: <c>Ns.Type.Method Controllers/HomeController.cs:42</c>,
/// the path relative to the working directory (or to the project folder named after the assembly).</item>
/// <item>The app's code without line info: <c>Ns.Type.Method MyApp/Type</c>.</item>
/// <item>NuGet packages: <c>Npgsql.NpgsqlCommand.ExecuteReader lib:Npgsql/NpgsqlCommand</c>.</item>
/// <item>The runtime and shared frameworks: <c>System.Text.Json.JsonSerializer.Serialize std:System.Text.Json/JsonSerializer</c>.</item>
/// <item>Frames with no managed method: <c>(native)</c>.</item>
/// </list>
/// Parameter lists and generic arguments are dropped, and async state machines read as the
/// method they belong to (<c>Type+&lt;Run&gt;d__3.MoveNext</c> becomes <c>Type.Run</c>).
/// </summary>
internal sealed class FrameNamer
{
    public const string Native = "(native)";

    private static readonly Regex AsyncMoveNext = new(@"\+<([A-Za-z0-9_]+)>d(?:__\d+)?(?:`\d+)?\.MoveNext$", RegexOptions.CultureInvariant);

    private readonly CodeOwners _owners;
    private readonly string _workDir;

    public FrameNamer(CodeOwners owners, string workingDirectory)
    {
        _owners = owners;
        _workDir = workingDirectory.Replace('\\', '/').TrimEnd('/') + "/";
    }

    public CodeKind KindOf(string? module, string? modulePath) =>
        string.IsNullOrEmpty(module) ? CodeKind.Std : _owners.Of(module, modulePath).Kind;

    /// <param name="fullMethod">TraceEvent's FullMethodName, e.g. <c>Ns.Type.Method(int32)</c>.</param>
    /// <param name="module">The assembly's simple name, or null for native code.</param>
    /// <param name="modulePath">Where the assembly was loaded from, when known.</param>
    /// <param name="sourceFile">The build-time source path from the PDB, when resolved.</param>
    /// <param name="line">The source line, when resolved.</param>
    public string Name(string? fullMethod, string? module, string? modulePath, string? sourceFile = null, int line = 0)
    {
        var method = CleanMethod(fullMethod);
        if (method.Length == 0 || string.IsNullOrEmpty(module))
        {
            return method.Length == 0 ? Native : method;
        }
        if (IsDynamic(module))
        {
            // Code generated at run time (expression trees, the minimal API glue): no file.
            return method;
        }
        var (kind, package) = _owners.Of(module, modulePath);
        string file = kind switch
        {
            CodeKind.App when !string.IsNullOrEmpty(sourceFile) && line > 0 => $"{RelativeSource(sourceFile, module)}:{line}",
            CodeKind.App => $"{module}/{TypeOf(method)}",
            CodeKind.Lib => $"lib:{package}/{TypeOf(method)}",
            _ => $"std:{module}/{TypeOf(method)}",
        };
        return (method + " " + file.Replace(' ', '_')).Replace(';', ',');
    }

    /// <summary>A frame name for a caller: "fn file:line" becomes "fn file".</summary>
    public static string WithoutLine(string name)
    {
        var colon = name.LastIndexOf(':');
        if (colon <= 0 || colon == name.Length - 1 || name.IndexOf(' ') < 0 || name.IndexOf(' ') > colon)
        {
            return name;
        }
        for (var i = colon + 1; i < name.Length; i++)
        {
            if (!char.IsAsciiDigit(name[i]))
            {
                return name;
            }
        }
        return name[..colon];
    }

    /// <summary>The module that holds DynamicMethods and other code emitted at run time.</summary>
    public static bool IsDynamic(string module) =>
        module.Contains("DynamicMethods", StringComparison.Ordinal) || module.StartsWith("Anonymously Hosted", StringComparison.Ordinal);

    /// <summary>The method without its parameter list, generic arguments and spaces.</summary>
    public static string CleanMethod(string? full)
    {
        if (string.IsNullOrEmpty(full) || full == "?" || full.EndsWith("!?", StringComparison.Ordinal))
        {
            return "";
        }
        var b = new StringBuilder(full.Length);
        var depth = 0;
        foreach (var c in full)
        {
            if (c == '(' && depth == 0)
            {
                break;
            }
            if (c == '[')
            {
                depth++;
            }
            else if (c == ']')
            {
                depth = Math.Max(0, depth - 1);
            }
            else if (depth == 0 && c != ' ')
            {
                b.Append(c);
            }
        }
        var s = AsyncMoveNext.Replace(b.ToString(), ".$1");
        return s.Replace(';', ',');
    }

    /// <summary>The outermost type's simple name: <c>Ns.Outer+Inner.Method</c> gives <c>Outer</c>.</summary>
    public static string TypeOf(string method)
    {
        var dot = method.LastIndexOf('.');
        if (dot <= 0)
        {
            return method;
        }
        var type = method[..dot].Split('+')[0];
        type = type[(type.LastIndexOf('.') + 1)..];
        var tick = type.IndexOf('`');
        return tick > 0 ? type[..tick] : type;
    }

    /// <summary>
    /// A source path as the app knows it: relative to the working directory, else after the
    /// folder named after the assembly (the project folder), else after SourceLink's /_/ root,
    /// else the file name.
    /// </summary>
    public string RelativeSource(string path, string module)
    {
        var p = path.Replace('\\', '/');
        if (p.StartsWith(_workDir, StringComparison.OrdinalIgnoreCase))
        {
            return p[_workDir.Length..];
        }
        var project = "/" + module + "/";
        var at = p.LastIndexOf(project, StringComparison.OrdinalIgnoreCase);
        if (at >= 0)
        {
            return p[(at + project.Length)..];
        }
        if (p.StartsWith("/_/", StringComparison.Ordinal))
        {
            return p[3..];
        }
        return Path.GetFileName(p);
    }
}

/// <summary>Which samples count: threads using the CPU in code that isn't the SDK's own.</summary>
internal static class SampleFilter
{
    /// <summary>Leaves that mean a thread is waiting, even when sampled in managed code on its way in or out.</summary>
    private static readonly string[] WaitLeaves =
    {
        "System.Threading.Monitor.Wait", "System.Threading.Monitor.ReliableEnter", "System.Threading.Monitor.Enter",
        "System.Threading.WaitHandle.Wait", "System.Threading.Thread.Sleep", "System.Threading.Thread.Join",
        "System.Threading.LowLevelLifoSemaphore.Wait", "System.Threading.LowLevelMonitor.Wait",
        "System.Threading.ManualResetEventSlim.Wait", "System.Threading.SemaphoreSlim.Wait",
        "System.Threading.WaitSubsystem.", "System.Threading.PortableThreadPool+GateThread.",
        "System.Threading.PortableThreadPool+IOCompletionPoller.Poll", "System.Threading.Thread.<PollGC>",
        "Interop+Kernel32.GetMessage",
        "Interop+Kernel32.GetQueuedCompletionStatus", "Interop+Kernel32.WaitFor", "Interop+Kernel32.SleepEx",
        "Interop+Sys.WaitForSocketEvents", "Interop+Sys.Poll", "Interop+Sys.Read", "Interop+Sys.LowLevelMonitor_",
        "Interop+Kernel32.ReadFile", "Interop+Winsock.", "Interop+Sys.Accept", "Interop+Sys.Receive",
    };

    /// <summary>Modules whose frames mark the SDK's own profiling work.</summary>
    private static readonly HashSet<string> SdkModules = new(StringComparer.OrdinalIgnoreCase)
    {
        "Visionary.AspNetCore.Profiling", "Microsoft.Diagnostics.NETCore.Client",
        "Microsoft.Diagnostics.Tracing.TraceEvent", "Microsoft.Diagnostics.FastSerialization",
    };

    /// <summary>
    /// Types of Visionary.AspNetCore that only run on its background services (writer, system
    /// sampler, retention, store); request-path types such as the middleware are app time.
    /// </summary>
    private static readonly HashSet<string> SdkBackgroundTypes = new(StringComparer.Ordinal)
    {
        "TelemetryWriter", "RowBuilder", "Retention", "SqlStore", "D1Store", "StoreFactory", "StoreExtensions",
        "Schema", "SystemSampler", "HostSampler", "CgroupReader", "ProcFs", "Native", "Redactor", "ErrorFields",
    };

    public static bool IsWaitLeaf(string method)
    {
        foreach (var w in WaitLeaves)
        {
            if (method.StartsWith(w, StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }

    /// <summary>A frame of the SDK's own background work (or of the profiler itself).</summary>
    public static bool IsSdkFrame(string method, string? module)
    {
        if (module is not null && SdkModules.Contains(module))
        {
            return true;
        }
        const string ns = "Styropyr0.Visionary.";
        if (!method.StartsWith(ns, StringComparison.Ordinal))
        {
            return false;
        }
        var rest = method.AsSpan(ns.Length);
        var end = rest.IndexOfAny('.', '+');
        var type = (end < 0 ? rest : rest[..end]).ToString();
        return SdkBackgroundTypes.Contains(type) || type == "Profiling";
    }
}

/// <summary>Folded stacks, heaviest first, capped to 90 000 bytes with the rest rolled up into their callers.</summary>
internal static class Folder
{
    public const int MaxFolded = 90_000;
    public const int KeepWhole = 72_000;
    /// <summary>A scheduled profile with fewer samples is dropped; a "Profile now" one is kept with any.</summary>
    public const int MinSamples = 20;

    /// <summary>
    /// Whole lines up to <paramref name="keepWhole"/>, then the rest cut after the deepest frame
    /// they share with a kept stack and ending in "(other)", then one "(other) n" for whatever is
    /// left (sdks/SPEC.md, "CPU profiles").
    /// </summary>
    /// <param name="lines">Each distinct stack ("outer;...;leaf") and its sample count.</param>
    /// <param name="maxBytes">The cap in UTF-8 bytes.</param>
    /// <param name="keepWhole">The bytes the heaviest stacks are kept whole within.</param>
    public static string Fold(IReadOnlyDictionary<string, long> lines, int maxBytes = MaxFolded, int keepWhole = KeepWhole)
    {
        var sorted = HeaviestFirst(lines);
        var b = new StringBuilder();
        var size = 0;
        // Whole lines first, leaving room for the rolled-up ones.
        var i = 0;
        for (; i < sorted.Count; i++)
        {
            var line = $"{sorted[i].Key} {sorted[i].Value}\n";
            var bytes = Encoding.UTF8.GetByteCount(line);
            if (size + bytes > keepWhole)
            {
                break;
            }
            b.Append(line);
            size += bytes;
        }
        if (i == sorted.Count)
        {
            return b.ToString();
        }
        // The rest are cut after the deepest frame they share with a kept stack
        // and end in "(other)", so their time stays under the right caller.
        var prefixes = new HashSet<string>(StringComparer.Ordinal);
        foreach (var (k, _) in sorted.Take(i))
        {
            for (var j = k.IndexOf(';'); j >= 0; j = k.IndexOf(';', j + 1))
            {
                prefixes.Add(k[..j]);
            }
            prefixes.Add(k);
        }
        var rolled = new Dictionary<string, long>(StringComparer.Ordinal);
        long other = 0;
        foreach (var (k, n) in sorted.Skip(i))
        {
            var cut = "";
            for (var j = k.IndexOf(';'); j >= 0; j = k.IndexOf(';', j + 1))
            {
                if (prefixes.Contains(k[..j]))
                {
                    cut = k[..j];
                }
            }
            if (prefixes.Contains(k))
            {
                cut = k; // the whole stack leads into a kept one: nothing to cut
            }
            if (cut.Length == 0)
            {
                other += n;
                continue;
            }
            var key = cut == k ? k : cut + ";(other)";
            rolled[key] = rolled.GetValueOrDefault(key) + n;
        }
        foreach (var (k, n) in HeaviestFirst(rolled))
        {
            var line = $"{k} {n}\n";
            var bytes = Encoding.UTF8.GetByteCount(line);
            if (size + bytes > maxBytes - 32) // room for the last "(other) n"
            {
                other += n;
                continue;
            }
            b.Append(line);
            size += bytes;
        }
        if (other > 0)
        {
            b.Append("(other) ").Append(other).Append('\n');
        }
        return b.ToString();
    }

    /// <summary>Heaviest first, ties in text order.</summary>
    private static List<KeyValuePair<string, long>> HeaviestFirst(IEnumerable<KeyValuePair<string, long>> lines) =>
        lines.OrderByDescending(kv => kv.Value).ThenBy(kv => kv.Key, StringComparer.Ordinal).ToList();
}

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>Writes the NuGet packages in the app's deps.json to app_packages once, 30 seconds after start</summary>
internal sealed class PackageReporter : BackgroundService
{
    private static readonly TimeSpan Delay = TimeSpan.FromSeconds(30);
    internal const int Max = 5000;

    private readonly VisionaryAgent _agent;

    public PackageReporter(VisionaryAgent agent) => _agent = agent;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (!_agent.Enabled || !_agent.Options.Packages)
        {
            return;
        }
        Suppress.Enter();
        try
        {
            await Task.Delay(Delay, stoppingToken).ConfigureAwait(false);
            await ReportAsync(DepsFile(), stoppingToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
        {
        }
        catch (Exception e)
        {
            _agent.Logger.LogError("visionary: write packages failed: {Error}", e.Message);
        }
    }

    internal static string? DepsFile()
    {
        var name = Assembly.GetEntryAssembly()?.GetName().Name;
        return name is null ? null : Path.Combine(AppContext.BaseDirectory, name + ".deps.json");
    }

    /// <summary>The libraries of type package, as name and version, at most <see cref="Max"/></summary>
    internal static List<(string Name, string Version)> Read(string json)
    {
        var output = new List<(string Name, string Version)>();
        using var doc = JsonDocument.Parse(json);
        if (!doc.RootElement.TryGetProperty("libraries", out var libraries) || libraries.ValueKind != JsonValueKind.Object)
        {
            return output;
        }
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var lib in libraries.EnumerateObject())
        {
            if (lib.Value.ValueKind != JsonValueKind.Object || !lib.Value.TryGetProperty("type", out var type)
                || type.ValueKind != JsonValueKind.String || type.GetString() != "package")
            {
                continue;
            }
            var slash = lib.Name.IndexOf('/');
            if (slash <= 0 || slash == lib.Name.Length - 1 || !seen.Add(lib.Name))
            {
                continue;
            }
            output.Add((lib.Name[..slash], lib.Name[(slash + 1)..]));
            if (output.Count == Max)
            {
                break;
            }
        }
        return output;
    }

    internal async Task ReportAsync(string? path, CancellationToken ct)
    {
        if (path is null || !File.Exists(path))
        {
            return;
        }
        var packages = Read(await File.ReadAllTextAsync(path, ct).ConfigureAwait(false));
        if (packages.Count == 0)
        {
            return;
        }
        var store = _agent.Store!;
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(60));
        await store.CreateAsync("telemetry", null, null, timeout.Token).ConfigureAwait(false);
        var insert = Schema.InsertSql("app_packages", Schema.PackageColumns, store.StringParams);
        var at = RowBuilder.Timestamp(DateTime.UtcNow);
        var stmts = packages.Select(p => new Stmt(insert, new object?[]
        {
            at, _agent.Service, _agent.Instance, _agent.Version, "NuGet", p.Name, p.Version,
        })).ToList();
        var combined = Batches.Combine(store, stmts);
        for (var i = 0; i < combined.Count; i += Batches.D1Size)
        {
            var part = combined.GetRange(i, Math.Min(Batches.D1Size, combined.Count - i));
            await store.BatchAsync(part, timeout.Token).ConfigureAwait(false);
            _agent.Writes.Wrote(Batches.RowCount(part));
        }
    }
}

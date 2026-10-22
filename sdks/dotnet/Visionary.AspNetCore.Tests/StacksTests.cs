using System.Collections.Generic;
using System.Linq;
using System.Text;
using Styropyr0.Visionary.Profiling;
using Xunit;

namespace Styropyr0.Visionary.Tests;

public class StacksTests
{
    private const string Deps = """
    {
      "targets": {
        ".NETCoreApp,Version=v10.0": {
          "MyApp/1.0.0": { "dependencies": { "Npgsql": "9.0.0" }, "runtime": { "MyApp.dll": {} } },
          "MyApp.Domain/1.0.0": { "runtime": { "MyApp.Domain.dll": {} } },
          "Npgsql/9.0.0": { "runtime": { "lib/net8.0/Npgsql.dll": {} } },
          "Microsoft.EntityFrameworkCore/9.0.0": { "runtime": { "lib/net8.0/Microsoft.EntityFrameworkCore.dll": {} } }
        }
      },
      "libraries": {
        "MyApp/1.0.0": { "type": "project" },
        "MyApp.Domain/1.0.0": { "type": "project" },
        "Npgsql/9.0.0": { "type": "package" },
        "Microsoft.EntityFrameworkCore/9.0.0": { "type": "package" }
      }
    }
    """;

    private static FrameNamer Namer() =>
        new(new CodeOwners(Deps, "MyApp", "/usr/share/dotnet/shared", "/app"), "/src/MyApp");

    [Theory]
    [InlineData("System.Threading.Thread.Sleep(int32)", "System.Threading.Thread.Sleep")]
    [InlineData("System.Runtime.CompilerServices.AsyncTaskMethodBuilder`1+AsyncStateMachineBox`1[System.Threading.Tasks.VoidTaskResult,System.IO.Stream+<<CopyToAsync>g__Core|30_0>d].MoveNext(class System.Threading.Thread)",
        "System.Runtime.CompilerServices.AsyncTaskMethodBuilder`1+AsyncStateMachineBox`1.MoveNext")]
    [InlineData("MyApp.Handlers+<Compute>d__3.MoveNext()", "MyApp.Handlers.Compute")]
    [InlineData("System.Threading.AsyncOverSyncWithIoCancellation+<InvokeAsync>d__8`2[System.ValueTuple`2[System.__Canon,System.Memory`1[System.Byte]],System.Int32].MoveNext()",
        "System.Threading.AsyncOverSyncWithIoCancellation.InvokeAsync")]
    [InlineData("P+<>c.<Main>b__3_0()", "P+<>c.<Main>b__3_0")]
    [InlineData("?", "")]
    [InlineData("coreclr!?", "")]
    public void CleanMethod(string full, string want) => Assert.Equal(want, FrameNamer.CleanMethod(full));

    [Theory]
    [InlineData("System.Threading.PortableThreadPool+WorkerThread.WorkerThreadStart", "PortableThreadPool")]
    [InlineData("System.Collections.Generic.Dictionary`2.TryInsert", "Dictionary")]
    [InlineData("Main", "Main")]
    public void TypeOf(string method, string want) => Assert.Equal(want, FrameNamer.TypeOf(method));

    [Fact]
    public void NamesAppCodeWithLines()
    {
        var n = Namer();
        Assert.Equal("MyApp.Work.Burn Services/Work.cs:17",
            n.Name("MyApp.Work.Burn(int32)", "MyApp", "/app/MyApp.dll", "/src/MyApp/Services/Work.cs", 17));
        // Built elsewhere (a Docker stage): cut after the project folder.
        Assert.Equal("MyApp.Work.Burn Services/Work.cs:17",
            n.Name("MyApp.Work.Burn(int32)", "MyApp", "/app/MyApp.dll", "/build/repo/MyApp/Services/Work.cs", 17));
        // SourceLink's deterministic root.
        Assert.Equal("MyApp.Domain.Order.Total Order.cs:5",
            n.Name("MyApp.Domain.Order.Total()", "MyApp.Domain", "/app/MyApp.Domain.dll", "/_/Order.cs", 5));
        // Windows build path.
        Assert.Equal("MyApp.Work.Burn Services/Work.cs:9",
            new FrameNamer(new CodeOwners(Deps, "MyApp", null, null), @"C:\src\MyApp").Name(
                "MyApp.Work.Burn(int32)", "MyApp", null, @"C:\src\MyApp\Services\Work.cs", 9));
    }

    [Fact]
    public void NamesByOwner()
    {
        var n = Namer();
        Assert.Equal("MyApp.Work.Burn MyApp/Work", n.Name("MyApp.Work.Burn(int32)", "MyApp", "/app/MyApp.dll"));
        Assert.Equal("Npgsql.NpgsqlCommand.ExecuteReader lib:Npgsql/NpgsqlCommand",
            n.Name("Npgsql.NpgsqlCommand.ExecuteReader(value class System.Data.CommandBehavior)", "Npgsql", "/app/Npgsql.dll"));
        // A Microsoft.* NuGet package is a package, not the framework.
        Assert.Equal("Microsoft.EntityFrameworkCore.DbContext.SaveChanges lib:Microsoft.EntityFrameworkCore/DbContext",
            n.Name("Microsoft.EntityFrameworkCore.DbContext.SaveChanges()", "Microsoft.EntityFrameworkCore", "/app/Microsoft.EntityFrameworkCore.dll"));
        Assert.Equal("System.Text.Json.JsonSerializer.Serialize std:System.Text.Json/JsonSerializer",
            n.Name("System.Text.Json.JsonSerializer.Serialize(class System.Object)", "System.Text.Json",
                "/usr/share/dotnet/shared/Microsoft.NETCore.App/10.0.0/System.Text.Json.dll"));
        Assert.Equal("Microsoft.AspNetCore.Routing.EndpointMiddleware.Invoke std:Microsoft.AspNetCore.Routing/EndpointMiddleware",
            n.Name("Microsoft.AspNetCore.Routing.EndpointMiddleware.Invoke(class Microsoft.AspNetCore.Http.HttpContext)",
                "Microsoft.AspNetCore.Routing", "/usr/share/dotnet/shared/Microsoft.AspNetCore.App/10.0.0/Microsoft.AspNetCore.Routing.dll"));
        // Not in deps.json, not the framework, not next to the app: a package.
        Assert.Equal("Acme.Thing.Run lib:Acme/Thing", n.Name("Acme.Thing.Run()", "Acme", "/plugins/Acme.dll"));
        // Next to the app, not from a package: the app's.
        Assert.Equal("Acme.Thing.Run Acme/Thing", n.Name("Acme.Thing.Run()", "Acme", "/app/Acme.dll"));
        Assert.Equal(FrameNamer.Native, n.Name("?", null, null));
        Assert.Equal("dynamicClass.lambda_method1", n.Name("dynamicClass.lambda_method1(class System.Runtime.CompilerServices.Closure)",
            "Anonymously Hosted DynamicMethods Assembly", null));
        Assert.Equal("Odd.Name,With.Semicolon Odd/Name,With", n.Name("Odd.Name;With.Semicolon()", "Odd", "/app/Odd.dll"));
    }

    [Theory]
    [InlineData("MyApp.Work.Burn Services/Work.cs:17", "MyApp.Work.Burn Services/Work.cs")]
    [InlineData("Npgsql.NpgsqlCommand.ExecuteReader lib:Npgsql/NpgsqlCommand", "Npgsql.NpgsqlCommand.ExecuteReader lib:Npgsql/NpgsqlCommand")]
    [InlineData("System.Net.Http.Http2Connection.Send std:System.Net.Http/Http2Connection", "System.Net.Http.Http2Connection.Send std:System.Net.Http/Http2Connection")]
    [InlineData("(native)", "(native)")]
    [InlineData("dynamicClass.lambda_method1", "dynamicClass.lambda_method1")]
    public void CallersDropTheirLine(string name, string want) => Assert.Equal(want, FrameNamer.WithoutLine(name));

    [Fact]
    public void FiltersWaitsAndSdkWork()
    {
        Assert.True(SampleFilter.IsWaitLeaf("System.Threading.Monitor.Wait"));
        Assert.True(SampleFilter.IsWaitLeaf("System.Threading.WaitHandle.WaitOneNoCheck"));
        Assert.True(SampleFilter.IsWaitLeaf("Interop+Kernel32.GetQueuedCompletionStatus"));
        Assert.True(SampleFilter.IsWaitLeaf("System.Threading.LowLevelLifoSemaphore.WaitForSignal"));
        Assert.True(SampleFilter.IsWaitLeaf("System.Threading.Thread.<PollGC>g__PollGCWorker|70_0"));
        Assert.True(SampleFilter.IsWaitLeaf("System.Threading.PortableThreadPool+IOCompletionPoller.Poll"));
        Assert.False(SampleFilter.IsWaitLeaf("MyApp.Work.Burn"));
        Assert.True(SampleFilter.IsSdkFrame("Styropyr0.Visionary.TelemetryWriter.WriteAsync", "Visionary.AspNetCore"));
        Assert.True(SampleFilter.IsSdkFrame("Styropyr0.Visionary.SqlStore+Lease.DisposeAsync", "Visionary.AspNetCore"));
        Assert.True(SampleFilter.IsSdkFrame("Anything", "Microsoft.Diagnostics.NETCore.Client"));
        Assert.False(SampleFilter.IsSdkFrame("Styropyr0.Visionary.VisionaryMiddleware.InvokeAsync", "Visionary.AspNetCore"));
        Assert.False(SampleFilter.IsSdkFrame("MyApp.Work.Burn", "MyApp"));
    }

    [Fact]
    public void FoldsHeaviestFirst()
    {
        var folded = Folder.Fold(new Dictionary<string, long> { ["a;b"] = 3, ["a;c"] = 10, ["d"] = 3 });
        Assert.Equal("a;c 10\na;b 3\nd 3\n", folded);
    }

    [Fact]
    public void CapsFoldedBytes()
    {
        var lines = new Dictionary<string, long>();
        for (var i = 0; i < 5000; i++)
        {
            lines[$"Root.Main std:X/Y;App.Handler{i:D5} Handlers/Handler.cs:{i}"] = 5000 - i;
        }
        var total = lines.Values.Sum();
        var folded = Folder.Fold(lines);
        Assert.True(Encoding.UTF8.GetByteCount(folded) <= Folder.MaxFolded);
        var rows = folded.TrimEnd('\n').Split('\n');
        Assert.StartsWith("Root.Main std:X/Y;App.Handler00000 ", rows[0]);
        // Every stack starts at Root.Main, so the rest roll up under it.
        Assert.StartsWith("Root.Main std:X/Y;(other) ", rows[^1]);
        // No sample is lost: kept lines plus (other) add up to the total.
        Assert.Equal(total, rows.Sum(r => long.Parse(r[(r.LastIndexOf(' ') + 1)..])));
        var counts = rows.Where(r => !r.Contains("(other)")).Select(r => long.Parse(r[(r.LastIndexOf(' ') + 1)..])).ToList();
        Assert.Equal(counts.OrderByDescending(c => c), counts);
    }

    [Fact]
    public void CapCountsBytesNotChars()
    {
        var lines = new Dictionary<string, long> { ["é" + new string('x', 8) + " app.cs:1"] = 2, ["b"] = 1 };
        // The first line is 21 characters but 22 bytes ('é' takes two).
        Assert.Equal("(other) 3\n", Folder.Fold(lines, 100, 21));
        Assert.Equal("éxxxxxxxx app.cs:1 2\n(other) 1\n", Folder.Fold(lines, 100, 22));
    }

    // Stacks that don't fit are cut after the deepest frame they share with a
    // kept stack, so their time stays under the right caller.
    [Fact]
    public void FoldRollsUpIntoCallers()
    {
        var heavy = "serve;handler;" + new string('h', Folder.KeepWhole - 40);
        var lines = new Dictionary<string, long>
        {
            [heavy] = 100,
            ["serve;handler;json;encode;" + new string('e', 30_000)] = 7,
            ["serve;handler;json;encode;" + new string('f', 30_000)] = 5,
            ["serve;handler"] = 4, // leads into the kept stack: kept as it is
            ["worker;loop;" + new string('w', 30_000)] = 3,
        };
        var folded = Folder.Fold(lines);
        foreach (var want in new[] { heavy + " 100\n", "serve;handler;(other) 12\n", "serve;handler 4\n", "(other) 3\n" })
        {
            Assert.Contains(want, folded);
        }
        Assert.True(Encoding.UTF8.GetByteCount(folded) <= Folder.MaxFolded);
    }
}

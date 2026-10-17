using System;
using System.Data.Common;
using System.Linq;
using System.Reflection;
using System.Threading;
using System.Threading.Tasks;

namespace Styropyr0.Visionary;

/// <summary>
/// Picks where rows go, first match wins: the app's <see cref="DbDataSource"/> or connection
/// factory, VISIONARY_DATABASE_URL, then the D1_* variables. Drivers are optional: the SDK
/// looks them up at runtime and needs whichever one the URL names.
/// </summary>
internal static class StoreFactory
{
    private static readonly (string Invariant, string Type)[] PostgresDrivers =
    {
        ("Npgsql", "Npgsql.NpgsqlFactory, Npgsql"),
    };

    private static readonly (string Invariant, string Type)[] MySqlDrivers =
    {
        ("MySqlConnector", "MySqlConnector.MySqlConnectorFactory, MySqlConnector"),
        ("MySql.Data.MySqlClient", "MySql.Data.MySqlClient.MySqlClientFactory, MySql.Data"),
    };

    private static readonly (string Invariant, string Type)[] SqliteDrivers =
    {
        ("Microsoft.Data.Sqlite", "Microsoft.Data.Sqlite.SqliteFactory, Microsoft.Data.Sqlite"),
        ("System.Data.SQLite", "System.Data.SQLite.SQLiteFactory, System.Data.SQLite"),
    };

    /// <summary>Returns null when nothing is configured: collection is off.</summary>
    public static IStore? Create(VisionaryOptions o)
    {
        if (o.DataSource is { } ds)
        {
            var d = o.Dialect ?? Detect(ds.GetType()) ?? Detect(ds.CreateConnection().GetType())
                ?? throw new InvalidOperationException("visionary: set VisionaryOptions.Dialect for this data source");
            return new SqlStore(ct => ds.OpenConnectionAsync(ct), d, $"the application's {ds.GetType().Name} ({Name(d)})");
        }
        if (o.ConnectionFactory is { } factory)
        {
            var probe = factory();
            var d = o.Dialect ?? Detect(probe.GetType())
                ?? throw new InvalidOperationException("visionary: set VisionaryOptions.Dialect for this connection factory");
            probe.Dispose();
            return new SqlStore(ct => OpenAsync(factory(), ct), d, $"the application's connection factory ({Name(d)})");
        }
        var url = o.DatabaseUrl;
        if (!string.IsNullOrEmpty(url) && !url.StartsWith("d1:", StringComparison.OrdinalIgnoreCase))
        {
            return FromUrl(url);
        }
        if (string.IsNullOrEmpty(o.D1AccountId) || string.IsNullOrEmpty(o.D1DatabaseId) || string.IsNullOrEmpty(o.D1ApiToken))
        {
            if (!string.IsNullOrEmpty(url))
            {
                throw new InvalidOperationException("visionary: d1:// needs D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN");
            }
            return null;
        }
        return new D1Store(o.D1ApiBase, o.D1AccountId, o.D1DatabaseId, o.D1ApiToken);
    }

    private static IStore FromUrl(string raw)
    {
        var scheme = raw[..Math.Max(0, raw.IndexOf("://", StringComparison.Ordinal))].ToLowerInvariant();
        switch (scheme)
        {
            case "postgres" or "postgresql":
            {
                var f = Driver(PostgresDrivers, "PostgreSQL", "Npgsql");
                var cs = ServerConnectionString(f, new Uri(raw), 5432, "Host", "Port", "Username", "Password", "Database");
                return Open(f, cs, VisionaryDialect.Postgres, raw);
            }
            case "mysql" or "mariadb":
            {
                var f = Driver(MySqlDrivers, "MySQL", "MySqlConnector");
                var cs = ServerConnectionString(f, new Uri(raw), 3306, "Server", "Port", "User ID", "Password", "Database");
                return Open(f, cs, VisionaryDialect.MySql, raw);
            }
            case "sqlite" or "sqlite3":
            {
                var f = Driver(SqliteDrivers, "SQLite", "Microsoft.Data.Sqlite");
                var b = f.CreateConnectionStringBuilder() ?? new DbConnectionStringBuilder();
                b["Data Source"] = SqlitePath(raw, scheme);
                return Open(f, b.ConnectionString, VisionaryDialect.Sqlite, raw);
            }
            default:
                throw new InvalidOperationException($"visionary: unsupported database URL scheme \"{scheme}\"");
        }
    }

    /// <summary>sqlite:///abs/path.db is absolute (sqlite:///C:/x.db on Windows); sqlite://rel.db is relative.</summary>
    internal static string SqlitePath(string raw, string scheme)
    {
        var rest = raw[(scheme.Length + 3)..];
        var q = rest.IndexOf('?');
        if (q >= 0)
        {
            rest = rest[..q];
        }
        rest = Uri.UnescapeDataString(rest);
        if (!rest.StartsWith('/'))
        {
            return rest;
        }
        var path = rest.TrimStart('/');
        return path.Length > 1 && path[1] == ':' ? path : "/" + path;
    }

    private static string ServerConnectionString(DbProviderFactory f, Uri u, int defaultPort,
        string hostKey, string portKey, string userKey, string passwordKey, string databaseKey)
    {
        var b = f.CreateConnectionStringBuilder() ?? new DbConnectionStringBuilder();
        b[hostKey] = u.Host;
        b[portKey] = u.Port > 0 ? u.Port : defaultPort;
        var info = u.UserInfo.Split(':', 2);
        if (info[0].Length > 0)
        {
            b[userKey] = Uri.UnescapeDataString(info[0]);
        }
        if (info.Length > 1)
        {
            b[passwordKey] = Uri.UnescapeDataString(info[1]);
        }
        var db = Uri.UnescapeDataString(u.AbsolutePath.TrimStart('/'));
        if (db.Length > 0)
        {
            b[databaseKey] = db;
        }
        foreach (var pair in u.Query.TrimStart('?').Split('&', StringSplitOptions.RemoveEmptyEntries))
        {
            var kv = pair.Split('=', 2);
            var key = Uri.UnescapeDataString(kv[0]);
            var value = kv.Length > 1 ? Uri.UnescapeDataString(kv[1].Replace('+', ' ')) : "";
            if (key.Equals("sslmode", StringComparison.OrdinalIgnoreCase))
            {
                // libpq spells modes verify-full; the ADO.NET drivers spell them VerifyFull.
                key = hostKey == "Host" ? "SSL Mode" : "SslMode";
                value = value.Replace("-", "");
            }
            try
            {
                b[key] = value;
            }
            catch (ArgumentException)
            {
                // a libpq/JDBC-only option this driver doesn't know; leave it out
            }
        }
        return b.ConnectionString;
    }

    private static IStore Open(DbProviderFactory f, string cs, VisionaryDialect d, string raw) =>
        new SqlStore(ct =>
        {
            var conn = f.CreateConnection() ?? throw new InvalidOperationException("visionary: the driver returned no connection");
            conn.ConnectionString = cs;
            return OpenAsync(conn, ct);
        }, d, Redacted(raw) + $" ({Name(d)})");

    private static async ValueTask<DbConnection> OpenAsync(DbConnection conn, CancellationToken ct)
    {
        try
        {
            await conn.OpenAsync(ct).ConfigureAwait(false);
            return conn;
        }
        catch
        {
            await conn.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    }

    private static DbProviderFactory Driver((string Invariant, string Type)[] candidates, string database, string package)
    {
        foreach (var (invariant, typeName) in candidates)
        {
            if (DbProviderFactories.TryGetFactory(invariant, out var registered))
            {
                return registered;
            }
            var type = Type.GetType(typeName, throwOnError: false);
            var instance = type?.GetField("Instance", BindingFlags.Public | BindingFlags.Static)?.GetValue(null);
            if (instance is DbProviderFactory f)
            {
                return f;
            }
        }
        throw new InvalidOperationException($"visionary: {database} needs an ADO.NET driver; add the {package} package to the application");
    }

    /// <summary>The dialect of an ADO.NET type, from its namespace.</summary>
    internal static VisionaryDialect? Detect(Type t)
    {
        var n = (t.FullName ?? "").ToLowerInvariant();
        if (n.Contains("npgsql") || n.Contains("postgres"))
        {
            return VisionaryDialect.Postgres;
        }
        if (n.Contains("mysql") || n.Contains("maria"))
        {
            return VisionaryDialect.MySql;
        }
        if (n.Contains("sqlite"))
        {
            return VisionaryDialect.Sqlite;
        }
        return null;
    }

    private static string Name(VisionaryDialect d) => d.ToString().ToLowerInvariant();

    /// <summary>The URL without its password, for logs.</summary>
    private static string Redacted(string raw)
    {
        var scheme = raw.IndexOf("://", StringComparison.Ordinal);
        var at = raw.LastIndexOf('@');
        if (scheme < 0 || at < scheme)
        {
            return raw.Split('?').First();
        }
        var user = raw[(scheme + 3)..at].Split(':')[0];
        return (raw[..(scheme + 3)] + user + "@" + raw[(at + 1)..]).Split('?').First();
    }
}

using System;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary;

/// <summary>Statement sanitizing and the low-cardinality span names the dashboards group by.</summary>
internal static class SqlText
{
    public const int MaxDetail = 10000;

    private const RegexOptions Options = RegexOptions.Compiled | RegexOptions.CultureInvariant;

    private static readonly Regex StringLiteral = new("'(?:[^']|'')*'", Options);

    // Numbers that stand alone: not part of an identifier, a $1 placeholder or a parameter name.
    private static readonly Regex NumberLiteral = new(@"(?<![\w$@:.])-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?(?![\w.])", Options);

    // IN lists of literals or parameters: IN (?, ?), IN ($1, $2), IN (@p0, @p1).
    private static readonly Regex InList = new(@"(?i)\bIN\s*\(\s*(?:\?|\$\d+|[@:]\w+)(?:\s*,\s*(?:\?|\$\d+|[@:]\w+))*\s*\)", Options);

    private static readonly Regex Spaces = new(@"\s+", Options);

    private static readonly Regex FirstWord = new(@"^\W*([A-Za-z]+)", Options);
    private static readonly Regex FromTable = new(@"(?i)\bFROM\s+([^\s,;()]+)", Options);
    private static readonly Regex IntoTable = new(@"(?i)\bINTO\s+([^\s,;()]+)", Options);
    private static readonly Regex UpdateTable = new(@"(?i)^\W*UPDATE\s+(?:ONLY\s+)?([^\s,;()]+)", Options);
    private static readonly Regex MainVerb = new(@"(?i)\)\s*(SELECT|INSERT|UPDATE|DELETE)\b", Options);

    /// <summary>
    /// Literals become ?, IN lists collapse to IN (?), whitespace runs to one space. Parameter
    /// values are never part of the text, so nothing else needs scrubbing.
    /// </summary>
    public static string Sanitize(string sql)
    {
        var s = StringLiteral.Replace(sql, "?");
        s = NumberLiteral.Replace(s, "?");
        s = InList.Replace(s, "IN (?)");
        s = Spaces.Replace(s, " ").Trim();
        return s.Length > MaxDetail ? s[..MaxDetail] : s;
    }

    /// <summary>First keyword plus the main table: "SELECT users", "INSERT bookings", "BEGIN".</summary>
    public static string Name(string sql)
    {
        var m = FirstWord.Match(sql);
        if (!m.Success)
        {
            return "SQL";
        }
        var verb = m.Groups[1].Value.ToUpperInvariant();
        if (verb == "WITH")
        {
            var main = MainVerb.Match(sql);
            verb = main.Success ? main.Groups[1].Value.ToUpperInvariant() : "SELECT";
        }
        var table = verb switch
        {
            "SELECT" or "DELETE" => FromTable.Match(sql),
            "INSERT" or "REPLACE" => IntoTable.Match(sql),
            "UPDATE" => UpdateTable.Match(sql),
            _ => Match.Empty,
        };
        if (verb == "UPDATE" && !table.Success)
        {
            // WITH ... UPDATE t: the table follows the verb rather than starting the text.
            table = Regex.Match(sql, @"(?i)\bUPDATE\s+([^\s,;()]+)");
        }
        return table.Success ? verb + " " + Table(table.Groups[1].Value) : verb;
    }

    /// <summary>The bare table name: no schema, no quoting.</summary>
    private static string Table(string raw)
    {
        var t = raw.Trim('"', '`', '[', ']');
        var dot = t.LastIndexOf('.');
        if (dot >= 0)
        {
            t = t[(dot + 1)..];
        }
        return t.Trim('"', '`', '[', ']');
    }

    /// <summary>The database system of an ADO.NET command or connection type.</summary>
    public static string System(Type t)
    {
        var n = (t.FullName ?? "").ToLowerInvariant();
        return n switch
        {
            _ when n.Contains("npgsql") => "postgresql",
            _ when n.Contains("mysql") => "mysql",
            _ when n.Contains("sqlite") => "sqlite",
            _ when n.Contains("sqlclient") => "mssql",
            _ when n.Contains("oracle") => "oracle",
            _ => "db",
        };
    }
}

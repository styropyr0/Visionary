namespace Styropyr0.Visionary;

public static partial class Visionary
{
    /// <summary>How far the background writers lag behind</summary>
    public static VisionaryWriterStats WriterStats()
    {
        if (VisionaryAgent.Current is not { } agent)
        {
            return new VisionaryWriterStats(0, 0, 0);
        }
        var queued = agent.Queue.Reader.Count + (agent.Logs?.Queue.Reader.Count ?? 0);
        return new VisionaryWriterStats(queued, agent.Writes.Written, agent.Writes.Dropped);
    }
}

/// <summary>Writer totals across request rows, logs and metrics</summary>
/// <param name="Queued">Rows waiting for the database</param>
/// <param name="Written">Rows written since start</param>
/// <param name="Dropped">Rows lost to a full queue or a write that failed three times</param>
public sealed record VisionaryWriterStats(int Queued, long Written, long Dropped);

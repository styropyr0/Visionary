using System;
using System.Runtime.InteropServices;

namespace Styropyr0.Visionary;

/// <summary>
/// The few OS calls the runtime has no managed API for. Every wrapper returns null instead of
/// throwing, so a missing symbol or an unexpected platform only leaves a column NULL.
/// </summary>
internal static class Native
{
    public readonly record struct CpuTimes(ulong Idle, ulong Kernel, ulong User);

    public readonly record struct MemoryStatus(ulong TotalPhys, ulong AvailPhys, ulong TotalPageFile, ulong AvailPageFile);

    /// <summary>Whole-machine CPU time in 100 ns units; kernel includes idle (Windows).</summary>
    public static CpuTimes? SystemTimes()
    {
        if (!OperatingSystem.IsWindows())
        {
            return null;
        }
        try
        {
            return GetSystemTimes(out var idle, out var kernel, out var user) ? new CpuTimes(idle, kernel, user) : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    public static MemoryStatus? Memory()
    {
        if (!OperatingSystem.IsWindows())
        {
            return null;
        }
        try
        {
            var m = new MemoryStatusEx { Length = (uint)Marshal.SizeOf<MemoryStatusEx>() };
            return GlobalMemoryStatusEx(ref m) ? new MemoryStatus(m.TotalPhys, m.AvailPhys, m.TotalPageFile, m.AvailPageFile) : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    /// <summary>Bytes this process has read and written (Windows).</summary>
    public static (ulong Read, ulong Write)? ProcessIo(IntPtr process)
    {
        if (!OperatingSystem.IsWindows())
        {
            return null;
        }
        try
        {
            return GetProcessIoCounters(process, out var io) ? (io.ReadTransferCount, io.WriteTransferCount) : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    /// <summary>Inodes used, in percent, of the filesystem holding <paramref name="path"/> (64-bit Linux).</summary>
    public static double? InodesUsedPercent(string path)
    {
        if (!OperatingSystem.IsLinux() || !Environment.Is64BitProcess)
        {
            return null;
        }
        try
        {
            // glibc and musl share this layout on 64-bit Linux: f_files and f_ffree are the 6th and 7th words.
            if (statvfs(path, out var st) != 0 || st.Files == 0 || st.FilesFree > st.Files)
            {
                return null;
            }
            return 100.0 * (st.Files - st.FilesFree) / st.Files;
        }
        catch (Exception)
        {
            return null;
        }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct MemoryStatusEx
    {
        public uint Length;
        public uint MemoryLoad;
        public ulong TotalPhys;
        public ulong AvailPhys;
        public ulong TotalPageFile;
        public ulong AvailPageFile;
        public ulong TotalVirtual;
        public ulong AvailVirtual;
        public ulong AvailExtendedVirtual;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct IoCounters
    {
        public ulong ReadOperationCount;
        public ulong WriteOperationCount;
        public ulong OtherOperationCount;
        public ulong ReadTransferCount;
        public ulong WriteTransferCount;
        public ulong OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct StatVfs
    {
        public ulong BlockSize;
        public ulong FragmentSize;
        public ulong Blocks;
        public ulong BlocksFree;
        public ulong BlocksAvailable;
        public ulong Files;
        public ulong FilesFree;
        public ulong FilesAvailable;
        public ulong FsId;
        public ulong Flag;
        public ulong NameMax;
        public ulong Spare0;
        public ulong Spare1;
        public ulong Spare2;
        public ulong Spare3;
        public ulong Spare4;
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetSystemTimes(out ulong idleTime, out ulong kernelTime, out ulong userTime);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GlobalMemoryStatusEx(ref MemoryStatusEx buffer);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetProcessIoCounters(IntPtr process, out IoCounters counters);

    [DllImport("libc", SetLastError = true)]
    private static extern int statvfs([MarshalAs(UnmanagedType.LPUTF8Str)] string path, out StatVfs buffer);
}

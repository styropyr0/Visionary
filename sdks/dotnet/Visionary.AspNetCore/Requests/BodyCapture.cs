using System;
using System.IO;
using System.IO.Pipelines;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Http.Features;

namespace Styropyr0.Visionary;

/// <summary>Keeps the first <c>limit</c> bytes of a body and notes whether more came.</summary>
internal sealed class CaptureBuffer
{
    private readonly byte[] _buf;
    private int _len;

    public CaptureBuffer(int limit) => _buf = new byte[Math.Max(0, limit)];

    public bool Truncated { get; private set; }

    public void Append(ReadOnlySpan<byte> data)
    {
        var room = _buf.Length - _len;
        if (data.Length > room)
        {
            Truncated = true;
            data = data[..room];
        }
        data.CopyTo(_buf.AsSpan(_len));
        _len += data.Length;
    }

    /// <summary>The captured bytes as UTF-8, invalid sequences replaced.</summary>
    public string? Text() => _len == 0 ? null : Encoding.UTF8.GetString(_buf, 0, _len);
}

/// <summary>
/// Content types worth storing as text; binary and multipart payloads are useless there and
/// expensive to keep.
/// </summary>
internal static class Capturable
{
    private static readonly string[] Skipped =
    {
        "multipart/", "image/", "video/", "audio/", "application/octet-stream", "application/zip", "application/pdf",
    };

    public static bool Check(string? contentType)
    {
        if (string.IsNullOrEmpty(contentType))
        {
            return true;
        }
        foreach (var prefix in Skipped)
        {
            if (contentType.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
            {
                return false;
            }
        }
        return true;
    }
}

/// <summary>Tees up to the cap of the request body as the application reads it.</summary>
internal sealed class RequestCapture : Stream
{
    private readonly Stream _inner;
    private readonly CaptureBuffer _capture;

    public RequestCapture(Stream inner, int limit)
    {
        _inner = inner;
        _capture = new CaptureBuffer(limit);
    }

    public long Size { get; private set; }

    public bool Truncated => _capture.Truncated;

    public string? Text() => _capture.Text();

    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();
    public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }

    public override int Read(byte[] buffer, int offset, int count) => Seen(buffer.AsSpan(offset, _inner.Read(buffer, offset, count)));

    public override int Read(Span<byte> buffer) => Seen(buffer[.._inner.Read(buffer)]);

    public override async Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken ct) =>
        Seen(buffer.AsSpan(offset, await _inner.ReadAsync(buffer.AsMemory(offset, count), ct).ConfigureAwait(false)));

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken ct = default)
    {
        var n = await _inner.ReadAsync(buffer, ct).ConfigureAwait(false);
        return Seen(buffer.Span[..n]);
    }

    private int Seen(ReadOnlySpan<byte> data)
    {
        Size += data.Length;
        _capture.Append(data);
        return data.Length;
    }

    public override void Flush() { }
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
}

/// <summary>
/// Stands in for the response body feature: every byte goes straight through to the client
/// (flushes, StartAsync and DisableBuffering too, so streaming still streams) while the first
/// bytes up to the cap are copied aside and the rest is only counted.
/// </summary>
internal sealed class ResponseCapture : Stream, IHttpResponseBodyFeature
{
    private readonly IHttpResponseBodyFeature _inner;
    private readonly HttpResponse _response;
    private readonly bool _bodies;
    private readonly int _limit;
    private CaptureBuffer? _capture;
    private bool _decided;
    private PipeWriter? _writer;

    public ResponseCapture(IHttpResponseBodyFeature inner, HttpResponse response, bool bodies, int limit)
    {
        _inner = inner;
        _response = response;
        _bodies = bodies;
        _limit = limit;
    }

    public IHttpResponseBodyFeature Inner => _inner;

    public long Written { get; private set; }

    public bool Truncated => _capture?.Truncated ?? false;

    public string? Text() => _capture?.Text();

    Stream IHttpResponseBodyFeature.Stream => this;

    PipeWriter IHttpResponseBodyFeature.Writer => _writer ??= PipeWriter.Create(this, new StreamPipeWriterOptions(leaveOpen: true));

    /// <summary>Pushes anything the application left in the pipe adapter through to the client.</summary>
    public async Task FinishAsync()
    {
        if (_writer is not null)
        {
            await _writer.FlushAsync().ConfigureAwait(false);
        }
    }

    public Task StartAsync(CancellationToken cancellationToken = default) => _inner.StartAsync(cancellationToken);

    public void DisableBuffering() => _inner.DisableBuffering();

    public async Task CompleteAsync()
    {
        await FinishAsync().ConfigureAwait(false);
        await _inner.CompleteAsync().ConfigureAwait(false);
    }

    public async Task SendFileAsync(string path, long offset, long? count, CancellationToken cancellationToken = default)
    {
        if (Decide() is null)
        {
            // Not captured anyway: keep the server's zero-copy path and only count.
            Written += count ?? Math.Max(0, new FileInfo(path).Length - offset);
            await _inner.SendFileAsync(path, offset, count, cancellationToken).ConfigureAwait(false);
            return;
        }
        await SendFileFallback.SendFileAsync(this, path, offset, count, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Capture is decided on the first write, once the content type is known.</summary>
    private CaptureBuffer? Decide()
    {
        if (!_decided)
        {
            _decided = true;
            _capture = _bodies && Capturable.Check(_response.ContentType) ? new CaptureBuffer(_limit) : null;
        }
        return _capture;
    }

    private void Seen(ReadOnlySpan<byte> data)
    {
        Written += data.Length;
        Decide()?.Append(data);
    }

    public override void Write(byte[] buffer, int offset, int count)
    {
        _inner.Stream.Write(buffer, offset, count);
        Seen(buffer.AsSpan(offset, count));
    }

    public override void Write(ReadOnlySpan<byte> buffer)
    {
        _inner.Stream.Write(buffer);
        Seen(buffer);
    }

    public override async Task WriteAsync(byte[] buffer, int offset, int count, CancellationToken ct)
    {
        await _inner.Stream.WriteAsync(buffer.AsMemory(offset, count), ct).ConfigureAwait(false);
        Seen(buffer.AsSpan(offset, count));
    }

    public override async ValueTask WriteAsync(ReadOnlyMemory<byte> buffer, CancellationToken ct = default)
    {
        await _inner.Stream.WriteAsync(buffer, ct).ConfigureAwait(false);
        Seen(buffer.Span);
    }

    public override void Flush() => _inner.Stream.Flush();

    public override Task FlushAsync(CancellationToken ct) => _inner.Stream.FlushAsync(ct);

    public override bool CanRead => false;
    public override bool CanSeek => false;
    public override bool CanWrite => true;
    public override long Length => throw new NotSupportedException();
    public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
    public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
}

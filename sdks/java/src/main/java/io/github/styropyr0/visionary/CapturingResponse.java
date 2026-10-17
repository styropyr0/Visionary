package io.github.styropyr0.visionary;

import java.io.IOException;
import java.io.PrintWriter;
import java.io.Writer;
import java.nio.ByteBuffer;
import java.nio.CharBuffer;
import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;

import jakarta.servlet.ServletOutputStream;
import jakarta.servlet.WriteListener;
import jakarta.servlet.http.HttpServletResponse;
import jakarta.servlet.http.HttpServletResponseWrapper;

/**
 * Passes every byte straight through to the client while copying the first
 * {@code limit} bytes aside and counting the rest.
 *
 * <p>Spring's {@code ContentCachingResponseWrapper} is deliberately not used:
 * it holds the entire body in memory until the end of the request and swallows
 * flushes, which breaks streaming and ignores the body cap.
 */
final class CapturingResponse extends HttpServletResponseWrapper {

    private final boolean bodies;
    private final int limit;
    private CaptureBuffer capture;
    private boolean decided;
    private volatile long written;
    private ServletOutputStream stream;
    private PrintWriter writer;

    CapturingResponse(HttpServletResponse response, boolean bodies, int limit) {
        super(response);
        this.bodies = bodies;
        this.limit = limit;
    }

    @Override
    public ServletOutputStream getOutputStream() throws IOException {
        if (stream == null) {
            stream = new TeeOutputStream(super.getOutputStream());
        }
        return stream;
    }

    @Override
    public PrintWriter getWriter() throws IOException {
        if (writer == null) {
            writer = new PrintWriter(new TeeWriter(super.getWriter(), charset()));
        }
        return writer;
    }

    @Override
    public void reset() {
        super.reset();
        forgetBody();
    }

    @Override
    public void resetBuffer() {
        super.resetBuffer();
        forgetBody();
    }

    byte[] body() {
        CaptureBuffer c = capture;
        return c == null ? new byte[0] : c.bytes();
    }

    boolean truncated() {
        CaptureBuffer c = capture;
        return c != null && c.truncated();
    }

    /** Bytes written, or the declared Content-Length if larger. */
    long size() {
        long declared = 0;
        String header = getHeader("Content-Length");
        if (header != null) {
            try {
                declared = Long.parseLong(header.trim());
            } catch (NumberFormatException ignored) {
                // a malformed header is the application's problem
            }
        }
        return Math.max(declared, written);
    }

    private synchronized void forgetBody() {
        decided = false;
        capture = null;
        written = 0;
    }

    /** The decision is made on the first write, once the content type is known. */
    private synchronized CaptureBuffer capture() {
        if (!decided) {
            decided = true;
            capture = bodies && HttpCapture.capturable(getContentType()) ? new CaptureBuffer(limit) : null;
        }
        return capture;
    }

    private Charset charset() {
        try {
            String enc = getCharacterEncoding();
            return enc == null ? StandardCharsets.ISO_8859_1 : Charset.forName(enc);
        } catch (RuntimeException e) {
            return StandardCharsets.UTF_8;
        }
    }

    private final class TeeOutputStream extends ServletOutputStream {
        private final ServletOutputStream out;

        TeeOutputStream(ServletOutputStream out) {
            this.out = out;
        }

        @Override
        public void write(int b) throws IOException {
            out.write(b);
            written++;
            CaptureBuffer c = capture();
            if (c != null) {
                c.write(b);
            }
        }

        @Override
        public void write(byte[] b, int off, int len) throws IOException {
            out.write(b, off, len);
            written += len;
            CaptureBuffer c = capture();
            if (c != null) {
                c.write(b, off, len);
            }
        }

        @Override
        public void flush() throws IOException {
            out.flush();
        }

        @Override
        public void close() throws IOException {
            out.close();
        }

        @Override
        public boolean isReady() {
            return out.isReady();
        }

        @Override
        public void setWriteListener(WriteListener listener) {
            out.setWriteListener(listener);
        }
    }

    private final class TeeWriter extends Writer {
        private final Writer out;
        private final Charset charset;
        private final boolean utf8;

        TeeWriter(Writer out, Charset charset) {
            this.out = out;
            this.charset = charset;
            this.utf8 = StandardCharsets.UTF_8.equals(charset);
        }

        @Override
        public void write(char[] cbuf, int off, int len) throws IOException {
            out.write(cbuf, off, len);
            observe(CharBuffer.wrap(cbuf, off, len));
        }

        @Override
        public void write(String str, int off, int len) throws IOException {
            out.write(str, off, len);
            observe(CharBuffer.wrap(str, off, off + len));
        }

        @Override
        public void write(int c) throws IOException {
            out.write(c);
            observe(CharBuffer.wrap(new char[] {(char) c}));
        }

        @Override
        public void flush() throws IOException {
            out.flush();
        }

        @Override
        public void close() throws IOException {
            out.close();
        }

        /** Copies the encoded bytes while there is room, and otherwise only counts them. */
        private void observe(CharBuffer chars) {
            CaptureBuffer c = capture();
            if (c != null && !c.truncated()) {
                ByteBuffer encoded = charset.encode(chars);
                byte[] b = new byte[encoded.remaining()];
                encoded.get(b);
                c.write(b, 0, b.length);
                written += b.length;
            } else if (utf8) {
                written += utf8Length(chars);
            } else {
                written += charset.encode(chars).remaining();
            }
        }

        private static long utf8Length(CharSequence s) {
            long n = 0;
            for (int i = 0; i < s.length(); i++) {
                char ch = s.charAt(i);
                if (ch < 0x80) {
                    n++;
                } else if (ch < 0x800) {
                    n += 2;
                } else if (Character.isHighSurrogate(ch) && i + 1 < s.length()
                        && Character.isLowSurrogate(s.charAt(i + 1))) {
                    n += 4;
                    i++;
                } else {
                    n += 3;
                }
            }
            return n;
        }
    }
}

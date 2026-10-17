package io.github.styropyr0.visionary;

import java.io.ByteArrayOutputStream;

/** Keeps the first {@code limit} bytes written to it and remembers whether more came. */
final class CaptureBuffer {

    private final int limit;
    private final ByteArrayOutputStream buf;
    private boolean truncated;

    CaptureBuffer(int limit) {
        this.limit = limit;
        this.buf = new ByteArrayOutputStream(Math.min(limit, 1024));
    }

    synchronized void write(int b) {
        if (buf.size() < limit) {
            buf.write(b);
        } else {
            truncated = true;
        }
    }

    synchronized void write(byte[] b, int off, int len) {
        int room = limit - buf.size();
        if (len > room) {
            truncated = true;
            len = Math.max(room, 0);
        }
        if (len > 0) {
            buf.write(b, off, len);
        }
    }

    synchronized byte[] bytes() {
        return buf.toByteArray();
    }

    synchronized boolean truncated() {
        return truncated;
    }
}

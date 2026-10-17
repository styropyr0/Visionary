package io.github.styropyr0.visionary

import java.io.ByteArrayOutputStream
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.util.zip.GZIPOutputStream

internal enum class SendResult {
    /** 2xx: the server took the batch. */
    SENT,

    /** 4xx: the payload or key is wrong (or rate limited); retrying won't help. */
    REJECTED,

    /** 5xx: try once more later. */
    SERVER_ERROR,

    /** No response at all: keep the events until the network is back. */
    OFFLINE,
}

/** Posts one JSON envelope to the ingest endpoint with HttpURLConnection, so the SDK needs no HTTP library. */
internal class Transport(endpoint: String, private val key: String, private val gzip: Boolean) {
    val url: URL = URL(ingestUrl(endpoint))

    fun send(body: String): Pair<SendResult, Int> {
        var conn: HttpURLConnection? = null
        return try {
            val raw = body.toByteArray(Charsets.UTF_8)
            val payload = if (gzip) gzip(raw) else raw
            conn = (url.openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                connectTimeout = TIMEOUT_MS
                readTimeout = TIMEOUT_MS
                doOutput = true
                useCaches = false
                setFixedLengthStreamingMode(payload.size)
                setRequestProperty("Content-Type", "application/json")
                setRequestProperty("Authorization", "Bearer $key")
                if (gzip) setRequestProperty("Content-Encoding", "gzip")
            }
            conn.outputStream.use { it.write(payload) }
            val code = conn.responseCode
            // Drain the body so the connection can be reused.
            (if (code >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes() }
            val result = when (code) {
                in 200..299 -> SendResult.SENT
                in 400..499 -> SendResult.REJECTED
                else -> SendResult.SERVER_ERROR
            }
            result to code
        } catch (_: IOException) {
            SendResult.OFFLINE to 0
        } finally {
            conn?.disconnect()
        }
    }

    private fun gzip(raw: ByteArray): ByteArray {
        val out = ByteArrayOutputStream(raw.size / 4 + 64)
        GZIPOutputStream(out).use { it.write(raw) }
        return out.toByteArray()
    }

    companion object {
        const val INGEST_PATH = "/api/ingest/client"
        private const val TIMEOUT_MS = 15_000

        fun ingestUrl(endpoint: String): String {
            val base = endpoint.trim().trimEnd('/')
            return if (base.endsWith(INGEST_PATH)) base else base + INGEST_PATH
        }
    }
}

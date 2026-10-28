package io.github.styropyr0.visionary

import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import kotlin.random.Random

/** Device-level envelope fields, read once at start. */
internal class Environment(val app: String, val device: String, val os: String, val appVersion: String?)

/**
 * The running SDK: builds events, queues them and sends them in batches on one background thread.
 * Every public entry point is cheap and never throws into the host app.
 */
internal class Client(
    private val config: VisionaryConfig,
    private val env: Environment,
    dir: File,
) {
    private val transport = Transport(config.endpoint, config.key, config.gzip)
    private val queue = EventQueue(File(dir, "queue.jsonl"), config.maxQueueSize.coerceAtLeast(1))
    private val sessionId = UUID.randomUUID().toString()
    private val sampled = Random.nextDouble() < config.sampleRate
    private val appVersion = config.appVersion?.takeIf { it.isNotBlank() } ?: env.appVersion

    @Volatile
    private var meta = Meta(sessionId, null, appVersion, config.version?.takeIf { it.isNotBlank() })

    @Volatile
    var currentScreen: String? = null

    private val worker: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "visionary").apply { isDaemon = true }
    }

    fun start() {
        worker.execute { safely { queue.restore() } }
        val every = config.flushIntervalMs.coerceAtLeast(1_000)
        worker.scheduleWithFixedDelay({ safely { flushNow() } }, every, every, TimeUnit.MILLISECONDS)
        log("started session $sessionId, sending to ${transport.url}")
    }

    fun setUser(id: String?) {
        val clean = id?.trim()?.take(200)?.replace(Regex("[\t\r\n]"), " ")?.ifEmpty { null }
        meta = meta.copy(userId = clean)
    }

    fun track(name: String, value: Double?, durationMs: Double?, attrs: Map<String, Any?>) {
        enqueue(event("custom", name).number("value", value).number("duration_ms", round(durationMs)).attrs("attrs", attrs))
    }

    fun screen(name: String, durationMs: Double, attrs: Map<String, Any?> = emptyMap()) {
        enqueue(event("screen", name).number("duration_ms", round(durationMs)).attrs("attrs", attrs))
    }

    fun api(method: String, url: String, path: String, status: Int, durationMs: Double, requestId: String? = null) {
        val m = method.uppercase(Locale.ROOT)
        val e = event("api", "$m ${Paths.normalize(path)}")
            .string("url", url)
            .string("method", m)
            .number("status", status)
            .number("duration_ms", round(durationMs))
        // The server's X-Request-ID links the call to its request and logs.
        requestId?.trim()?.take(128)?.takeIf { it.isNotEmpty() }?.let { e.attrs("attrs", mapOf("request_id" to it)) }
        enqueue(e)
    }

    fun logLine(level: String, message: String, attrs: Map<String, Any?>) {
        enqueue(event("log", level).string("message", message.take(8192)).attrs("attrs", attrs))
    }

    fun error(t: Throwable, handled: Boolean, attrs: Map<String, Any?>) {
        queue.add(Record(meta, errorEvent(t, handled, Thread.currentThread().name, attrs)))
        scheduleFlushIfFull()
    }

    /** Runs on the crashing thread: record and write to disk synchronously, the process is about to die. */
    fun crash(thread: Thread, t: Throwable) {
        queue.add(Record(meta, errorEvent(t, false, thread.name, emptyMap())))
        queue.persist()
    }

    fun flush() {
        worker.execute { safely { flushNow() } }
    }

    /** The app went to the background: send what we can and keep the rest on disk. */
    fun onBackground() {
        worker.execute {
            safely {
                flushNow()
                queue.persist()
            }
        }
    }

    private fun errorEvent(t: Throwable, handled: Boolean, thread: String, attrs: Map<String, Any?>): String {
        val all = LinkedHashMap<String, Any?>()
        all["handled"] = if (handled) 1 else 0
        all["thread"] = thread
        currentScreen?.let { all["screen"] = it }
        all.putAll(attrs)
        return event("error", t.javaClass.name)
            .string("message", (t.message ?: "").take(MAX_MESSAGE))
            .string("stack", t.stackTraceToString().take(MAX_STACK))
            .attrs("attrs", all)
            .build()
    }

    private fun event(kind: String, name: String) = JsonWriter()
        .string("ts", timestamp())
        .string("kind", kind)
        .string("name", name.take(MAX_NAME))

    private fun enqueue(e: JsonWriter) {
        if (!sampled) return
        queue.add(Record(meta, e.build()))
        scheduleFlushIfFull()
    }

    private fun scheduleFlushIfFull() {
        if (queue.size >= MAX_EVENTS) flush()
    }

    // Worker thread only.
    private fun flushNow() {
        while (true) {
            val batch = queue.nextBatch(MAX_EVENTS, MAX_EVENT_BYTES)
            if (batch.isEmpty()) return
            val (result, code) = transport.send(envelope(batch))
            when (result) {
                SendResult.SENT -> {
                    queue.remove(batch)
                    log("sent ${batch.size} events")
                }
                SendResult.REJECTED -> {
                    queue.remove(batch)
                    log("server rejected ${batch.size} events with $code; dropped")
                }
                SendResult.SERVER_ERROR -> {
                    if (batch.all { it.retried }) {
                        queue.remove(batch)
                        log("server answered $code again; dropped ${batch.size} events")
                    } else {
                        batch.forEach { it.retried = true }
                        log("server answered $code; will retry once")
                    }
                    return
                }
                SendResult.OFFLINE -> {
                    queue.persist()
                    log("offline; ${queue.size} events kept")
                    return
                }
            }
        }
    }

    private fun envelope(batch: List<Record>): String {
        val m = batch.first().meta
        val events = StringBuilder("[")
        batch.forEachIndexed { i, r ->
            if (i > 0) events.append(',')
            events.append(r.json)
        }
        events.append(']')
        return JsonWriter()
            .string("app", env.app)
            .string("app_version", m.appVersion)
            .string("version", m.version)
            .string("session_id", m.sessionId)
            .string("user_id", m.userId)
            .string("device", env.device)
            .string("os", env.os)
            .raw("events", events.toString())
            .build()
    }

    private inline fun safely(block: () -> Unit) {
        try {
            block()
        } catch (t: Throwable) {
            // A background failure must never stop the worker or reach the app.
            log("internal error: $t")
        }
    }

    fun log(message: String) {
        if (config.debug) {
            try {
                Log.d(TAG, message)
            } catch (_: Throwable) {
            }
        }
    }

    companion object {
        const val TAG = "Visionary"
        const val MAX_EVENTS = 100

        // The server refuses bodies over 64 KB; leave room for the envelope.
        const val MAX_EVENT_BYTES = 60_000
        private const val MAX_NAME = 300
        private const val MAX_MESSAGE = 1000
        private const val MAX_STACK = 16 * 1024

        private val format = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.ROOT).apply {
            timeZone = TimeZone.getTimeZone("UTC")
        }

        fun timestamp(): String = synchronized(format) { format.format(Date()) }

        fun round(ms: Double?): Double? = ms?.let { Math.round(it * 1000.0) / 1000.0 }
    }
}

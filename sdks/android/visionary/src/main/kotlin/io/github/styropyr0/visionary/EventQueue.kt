package io.github.styropyr0.visionary

import java.io.File

/** The envelope fields an event was recorded under. Events are only batched with equal meta. */
internal data class Meta(
    val sessionId: String,
    val userId: String?,
    val appVersion: String?,
    val version: String?,
)

/** One serialized event waiting to be sent. Identity equality on purpose: batches are removed by reference. */
internal class Record(val meta: Meta, val json: String) {
    val bytes: Int = json.toByteArray(Charsets.UTF_8).size
    var retried: Boolean = false
}

/**
 * Pending events, bounded, in memory, mirrored to [file] while the app is offline or in the background.
 * Thread-safe: the worker, the crash handler and callers on any thread use it.
 */
internal class EventQueue(private val file: File, private val capacity: Int) {
    private val records = ArrayDeque<Record>()
    private var mirrored = false

    @get:Synchronized
    val size: Int get() = records.size

    @Synchronized
    fun add(record: Record) {
        while (records.size >= capacity) records.removeFirst()
        records.addLast(record)
    }

    /** The next batch: leading events that share the first event's meta, within the count and size limits. */
    @Synchronized
    fun nextBatch(maxEvents: Int, maxBytes: Int): List<Record> {
        val first = records.firstOrNull() ?: return emptyList()
        val batch = ArrayList<Record>()
        var bytes = 0
        for (r in records) {
            if (r.meta != first.meta || batch.size >= maxEvents) break
            if (batch.isNotEmpty() && bytes + r.bytes + 1 > maxBytes) break
            batch.add(r)
            bytes += r.bytes + 1
        }
        return batch
    }

    @Synchronized
    fun remove(batch: List<Record>) {
        val gone = batch.toHashSet()
        records.removeAll { it in gone }
        if (mirrored) persist()
    }

    /** Writes every pending event to disk (atomically) so it survives the process; called off the main thread. */
    @Synchronized
    fun persist() {
        try {
            if (records.isEmpty()) {
                file.delete()
                mirrored = false
                return
            }
            file.parentFile?.mkdirs()
            val tmp = File(file.path + ".tmp")
            tmp.bufferedWriter(Charsets.UTF_8).use { w ->
                for (r in records) {
                    w.write(encode(r))
                    w.write('\n'.code)
                }
            }
            if (!tmp.renameTo(file)) {
                file.delete()
                tmp.renameTo(file)
            }
            mirrored = true
        } catch (_: Exception) {
            // Disk full or not writable: the events stay in memory.
        }
    }

    /** Loads events saved by a previous launch (including a crash report) ahead of new ones. */
    @Synchronized
    fun restore() {
        if (!file.exists()) return
        try {
            val restored = file.readLines(Charsets.UTF_8).mapNotNull(::decode)
            val current = records.toList()
            records.clear()
            restored.takeLast(capacity).forEach { records.addLast(it) }
            current.forEach { add(it) }
            mirrored = true
        } catch (_: Exception) {
            file.delete()
        }
    }

    // One record per line: four tab-separated meta fields, then the event JSON (which never holds a raw tab or newline).
    private fun encode(r: Record): String =
        listOf(r.meta.sessionId, r.meta.userId, r.meta.appVersion, r.meta.version)
            .joinToString("\t") { clean(it) } + "\t" + r.json

    private fun decode(line: String): Record? {
        val parts = line.split('\t', limit = 5)
        if (parts.size != 5 || !parts[4].startsWith("{")) return null
        fun field(i: Int) = parts[i].ifEmpty { null }
        val meta = Meta(parts[0], field(1), field(2), field(3))
        return Record(meta, parts[4])
    }

    private fun clean(s: String?): String =
        s?.replace('\t', ' ')?.replace('\n', ' ')?.replace('\r', ' ') ?: ""
}

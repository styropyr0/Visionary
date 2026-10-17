package io.github.styropyr0.visionary

/** A minimal JSON object writer, enough for the flat ingest payload without pulling in a library. */
internal class JsonWriter {
    private val sb = StringBuilder("{")
    private var empty = true

    fun string(key: String, value: String?): JsonWriter {
        if (value != null) {
            key(key)
            quote(value, sb)
        }
        return this
    }

    fun number(key: String, value: Number?): JsonWriter {
        val text = value?.let(::numberText) ?: return this
        key(key)
        sb.append(text)
        return this
    }

    /** Flat attributes: numbers stay numbers, everything else becomes a string. */
    fun attrs(key: String, attrs: Map<String, Any?>): JsonWriter {
        if (attrs.isEmpty()) return this
        val inner = JsonWriter()
        var count = 0
        for ((k, v) in attrs) {
            if (v == null || count >= MAX_ATTRS) continue
            when (v) {
                is Number -> inner.number(k.take(MAX_ATTR_KEY), v)
                is Boolean -> inner.string(k.take(MAX_ATTR_KEY), v.toString())
                else -> inner.string(k.take(MAX_ATTR_KEY), v.toString().take(MAX_ATTR_VALUE))
            }
            count++
        }
        if (count > 0) raw(key, inner.build())
        return this
    }

    fun raw(key: String, json: String): JsonWriter {
        key(key)
        sb.append(json)
        return this
    }

    fun build(): String = sb.append('}').toString()

    private fun key(key: String) {
        if (!empty) sb.append(',')
        empty = false
        quote(key, sb)
        sb.append(':')
    }

    companion object {
        private const val MAX_ATTRS = 50
        private const val MAX_ATTR_KEY = 100
        private const val MAX_ATTR_VALUE = 1000

        fun quote(s: String, sb: StringBuilder) {
            sb.append('"')
            for (c in s) {
                when (c) {
                    '"' -> sb.append("\\\"")
                    '\\' -> sb.append("\\\\")
                    '\n' -> sb.append("\\n")
                    '\r' -> sb.append("\\r")
                    '\t' -> sb.append("\\t")
                    '\b' -> sb.append("\\b")
                    '\u000C' -> sb.append("\\f")
                    else -> if (c < ' ' || c.code == 0x2028 || c.code == 0x2029) {
                        sb.append("\\u").append(Integer.toHexString(c.code).padStart(4, '0'))
                    } else {
                        sb.append(c)
                    }
                }
            }
            sb.append('"')
        }

        private fun numberText(n: Number): String? = when (n) {
            is Double, is Float -> {
                val d = n.toDouble()
                when {
                    d.isNaN() || d.isInfinite() -> null
                    d == Math.rint(d) && Math.abs(d) < 1e15 -> d.toLong().toString()
                    else -> d.toString()
                }
            }
            else -> n.toString()
        }
    }
}

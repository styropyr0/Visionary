package io.github.styropyr0.visionary

/**
 * Replaces id-like path segments with `:id` so API events group by route:
 * `/api/trips/8412/photos/3f2a9c1e-...` becomes `/api/trips/:id/photos/:id`.
 */
internal object Paths {
    private val uuid = Regex("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
    private val hex = Regex("^[0-9a-fA-F]{16,}$")
    private val token = Regex("^[A-Za-z0-9_-]{20,}$")
    private val hasDigit = Regex("[0-9]")

    fun normalize(path: String): String {
        if (path.isEmpty()) return "/"
        return path.split('/').joinToString("/") { if (isId(it)) ":id" else it }
    }

    private fun isId(segment: String): Boolean {
        if (segment.isEmpty()) return false
        if (segment.all { it in '0'..'9' }) return true
        if (uuid.matches(segment) || hex.matches(segment)) return true
        // Long opaque tokens (ULIDs, base64 ids) have digits; long words and slugs usually don't.
        return token.matches(segment) && hasDigit.containsMatchIn(segment)
    }
}

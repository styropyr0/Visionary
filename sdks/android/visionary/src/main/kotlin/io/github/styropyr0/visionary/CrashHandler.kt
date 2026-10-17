package io.github.styropyr0.visionary

/**
 * Records an uncaught exception to disk (it is sent on the next launch), then hands it to the
 * handler that was installed before, so the app crashes exactly as it would without the SDK.
 */
internal class CrashHandler(
    private val client: Client,
    private val previous: Thread.UncaughtExceptionHandler?,
) : Thread.UncaughtExceptionHandler {
    @Volatile
    private var handling = false

    override fun uncaughtException(thread: Thread, error: Throwable) {
        if (!handling) {
            handling = true
            try {
                client.crash(thread, error)
            } catch (_: Throwable) {
            }
        }
        if (previous != null) {
            previous.uncaughtException(thread, error)
        } else {
            // What ThreadGroup does when no default handler is set.
            System.err.print("Exception in thread \"${thread.name}\" ")
            error.printStackTrace()
        }
    }

    companion object {
        fun install(client: Client) {
            val previous = Thread.getDefaultUncaughtExceptionHandler()
            if (previous is CrashHandler) return
            Thread.setDefaultUncaughtExceptionHandler(CrashHandler(client, previous))
        }
    }
}

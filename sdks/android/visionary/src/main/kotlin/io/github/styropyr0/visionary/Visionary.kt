package io.github.styropyr0.visionary

import android.app.Activity
import android.app.Application
import android.content.Context
import android.util.Log
import androidx.annotation.AnyThread
import androidx.annotation.MainThread
import java.io.File

/**
 * Visionary for Android: screen timings, API timings, errors, crashes and custom events, sent in
 * batches to a Visionary server. Call [init] once from `Application.onCreate`; every other call is
 * a no-op until then and never throws.
 */
public object Visionary {
    @Volatile
    internal var client: Client? = null
        private set

    private var tracker: LifecycleTracker? = null

    @JvmStatic
    @MainThread
    public fun init(context: Context, config: VisionaryConfig) {
        if (client != null) return
        try {
            val app = context.applicationContext ?: context
            val c = Client(config, DeviceInfo.environment(app), File(app.noBackupFilesDir, "visionary"))
            if (config.captureCrashes) CrashHandler.install(c)
            if (app is Application) {
                tracker = LifecycleTracker(c, config.trackScreens).also(app::registerActivityLifecycleCallbacks)
            }
            c.start()
            client = c
        } catch (t: Throwable) {
            Log.w(Client.TAG, "Visionary could not start; collection is off", t)
        }
    }

    /** Attaches a user id to events recorded from now on; `null` clears it. */
    @JvmStatic
    @AnyThread
    public fun setUser(id: String?) {
        client?.setUser(id)
    }

    /** Records a custom event. Attribute values should be strings or numbers. */
    @JvmStatic
    @JvmOverloads
    @AnyThread
    public fun track(
        name: String,
        value: Double? = null,
        durationMs: Double? = null,
        attrs: Map<String, Any?> = emptyMap(),
    ) {
        client?.track(name, value, durationMs, attrs)
    }

    /** Records a handled exception (`attrs.handled = 1`). */
    @JvmStatic
    @JvmOverloads
    @AnyThread
    public fun captureException(error: Throwable, attrs: Map<String, Any?> = emptyMap()) {
        client?.error(error, handled = true, attrs = attrs)
    }

    /** Records a screen timing yourself, e.g. for Compose destinations or fragments. */
    @JvmStatic
    @JvmOverloads
    @AnyThread
    public fun trackScreen(name: String, durationMs: Double, attrs: Map<String, Any?> = emptyMap()) {
        client?.screen(name, durationMs, attrs)
    }

    /**
     * Call when the activity's real content is on screen (after data has loaded). Forwards to
     * [Activity.reportFullyDrawn] and records a `fully_drawn` event with the time since create.
     */
    @JvmStatic
    @MainThread
    public fun reportFullyDrawn(activity: Activity) {
        try {
            activity.reportFullyDrawn()
        } catch (_: SecurityException) {
            // Some API 24-28 builds require UPDATE_DEVICE_STATS for it; the timing is still ours.
        }
        tracker?.fullyDrawn(activity)
    }

    /** Sends queued events now, on the SDK's background thread. */
    @JvmStatic
    @AnyThread
    public fun flush() {
        client?.flush()
    }
}

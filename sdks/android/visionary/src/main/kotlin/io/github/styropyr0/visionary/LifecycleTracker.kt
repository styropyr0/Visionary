package io.github.styropyr0.visionary

import android.app.Activity
import android.app.Application
import android.os.Bundle
import android.view.ViewTreeObserver
import java.util.WeakHashMap

/**
 * Screen timings (activity created to its first frame) and foreground/background detection, which
 * counts started activities the way ProcessLifecycleOwner does, without depending on it.
 * All callbacks run on the main thread.
 */
internal class LifecycleTracker(
    private val client: Client,
    private val trackScreens: Boolean,
) : Application.ActivityLifecycleCallbacks {
    private val createdAt = WeakHashMap<Activity, Long>()
    private val awaitingFullyDrawn = WeakHashMap<Activity, Long>()
    private var started = 0

    override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) {
        val now = System.nanoTime()
        if (trackScreens) createdAt[activity] = now
        awaitingFullyDrawn[activity] = now
    }

    /** Time from create to the app saying its content is ready; reported once per activity. */
    fun fullyDrawn(activity: Activity) {
        val start = awaitingFullyDrawn.remove(activity) ?: return
        val ms = (System.nanoTime() - start) / 1_000_000.0
        client.track("fully_drawn", null, ms, mapOf("screen" to screenName(activity)))
    }

    override fun onActivityStarted(activity: Activity) {
        started++
        // The decor view exists once the activity has set its content; attaching any earlier
        // would force it into existence and break requestWindowFeature() calls.
        val start = createdAt.remove(activity) ?: return
        val decor = activity.window?.decorView ?: return
        val listener = object : ViewTreeObserver.OnDrawListener {
            private var done = false

            override fun onDraw() {
                if (done) return
                done = true
                val ms = (System.nanoTime() - start) / 1_000_000.0
                client.screen(screenName(activity), ms)
                // Listeners can't be removed while the tree is dispatching onDraw.
                decor.post { decor.viewTreeObserver.removeOnDrawListener(this) }
            }
        }
        decor.viewTreeObserver.addOnDrawListener(listener)
    }

    override fun onActivityResumed(activity: Activity) {
        client.currentScreen = screenName(activity)
    }

    override fun onActivityPaused(activity: Activity) {}

    override fun onActivityStopped(activity: Activity) {
        started = (started - 1).coerceAtLeast(0)
        if (started == 0 && !activity.isChangingConfigurations) client.onBackground()
    }

    override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) {}

    override fun onActivityDestroyed(activity: Activity) {
        createdAt.remove(activity)
        awaitingFullyDrawn.remove(activity)
    }

    companion object {
        fun screenName(activity: Activity): String = activity.javaClass.simpleName.ifEmpty { activity.javaClass.name }
    }
}

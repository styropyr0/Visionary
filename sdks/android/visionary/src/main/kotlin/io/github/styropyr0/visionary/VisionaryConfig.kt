package io.github.styropyr0.visionary

/**
 * Settings for [Visionary.init].
 *
 * @param endpoint the Visionary server, e.g. `https://visionary.example.com`. The SDK posts to
 *   `<endpoint>/api/ingest/client` unless the URL already ends with that path.
 * @param key a public client key (Settings → API keys, scope Client ingest).
 * @param appVersion the user-facing version; defaults to the package's `versionName`.
 * @param version the build or commit the dashboards group releases by, e.g. a short git hash.
 * @param sampleRate fraction of app launches (sessions) that report events, 0.0 to 1.0.
 *   Errors and crashes are always reported.
 */
public class VisionaryConfig @JvmOverloads constructor(
    public val endpoint: String,
    public val key: String,
    public val appVersion: String? = null,
    public val version: String? = null,
    public val sampleRate: Double = 1.0,
) {
    /** How often queued events are sent while the app is in the foreground. */
    public var flushIntervalMs: Long = 10_000

    /** Events kept in memory and on disk while offline; the oldest are dropped beyond this. */
    public var maxQueueSize: Int = 500

    /** Report time to first frame for every activity. */
    public var trackScreens: Boolean = true

    /** Install an uncaught exception handler (the previous handler still runs). */
    public var captureCrashes: Boolean = true

    /** Compress request bodies with `Content-Encoding: gzip`. Only enable it when the server accepts gzip. */
    public var gzip: Boolean = false

    /** Log what the SDK does to Logcat under the tag `Visionary`. */
    public var debug: Boolean = false
}

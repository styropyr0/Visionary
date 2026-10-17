package io.github.styropyr0.visionary

import android.content.Context
import android.os.Build

internal object DeviceInfo {
    fun environment(context: Context): Environment {
        val model = Build.MODEL?.trim().orEmpty().ifEmpty { "Android device" }
        val os = "Android ${Build.VERSION.RELEASE ?: Build.VERSION.SDK_INT}"
        return Environment("android", model, os, versionName(context))
    }

    @Suppress("DEPRECATION")
    private fun versionName(context: Context): String? = try {
        context.packageManager.getPackageInfo(context.packageName, 0).versionName
    } catch (_: Exception) {
        null
    }
}

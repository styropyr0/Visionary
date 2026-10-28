package io.github.styropyr0.visionary

import okhttp3.HttpUrl
import okhttp3.Interceptor
import okhttp3.Response
import java.io.IOException

/**
 * Reports every OkHttp call as an `api` event: method, path with ids replaced by `:id`, status
 * (0 when the call failed without a response) and the time until the response headers arrived.
 *
 * ```
 * val client = OkHttpClient.Builder().addInterceptor(VisionaryInterceptor()).build()
 * ```
 *
 * Add it with `addInterceptor` to time each logical call including redirects and retries, or with
 * `addNetworkInterceptor` to time each individual network request.
 */
public class VisionaryInterceptor : Interceptor {
    @Throws(IOException::class)
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val url = request.url
        val start = System.nanoTime()
        val response = try {
            chain.proceed(request)
        } catch (e: IOException) {
            record(request.method, url, 0, start, null)
            throw e
        }
        record(request.method, url, response.code, start, response.header("X-Request-ID"))
        return response
    }

    private fun record(method: String, url: HttpUrl, status: Int, start: Long, requestId: String?) {
        val client = Visionary.client ?: return
        if (url.encodedPath.endsWith(Transport.INGEST_PATH)) return
        val ms = (System.nanoTime() - start) / 1_000_000.0
        val port = if (url.port == HttpUrl.defaultPort(url.scheme)) "" else ":${url.port}"
        // No query string or credentials: they often carry tokens and personal data.
        val clean = "${url.scheme}://${url.host}$port${url.encodedPath}"
        client.api(method, clean, url.encodedPath, status, ms, requestId)
    }
}

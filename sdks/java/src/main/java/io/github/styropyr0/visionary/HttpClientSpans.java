package io.github.styropyr0.visionary;

import java.io.IOException;

import org.springframework.http.HttpRequest;
import org.springframework.http.client.ClientHttpRequestExecution;
import org.springframework.http.client.ClientHttpRequestInterceptor;
import org.springframework.http.client.ClientHttpResponse;

/**
 * An {@code http} span per {@code RestTemplate} or {@code RestClient} call, timed
 * until the response headers arrive. A transport error or a 5xx marks it failed.
 */
final class HttpClientSpans implements ClientHttpRequestInterceptor {

    @Override
    public ClientHttpResponse intercept(HttpRequest request, byte[] body, ClientHttpRequestExecution execution)
            throws IOException {
        VisionarySpan span = Telemetry.startHttp(request.getMethod().name(), request.getURI());
        if (span == null) {
            return execution.execute(request, body);
        }
        try {
            ClientHttpResponse response = execution.execute(request, body);
            Telemetry.httpStatus(span, response.getStatusCode().value());
            return response;
        } catch (IOException | RuntimeException | Error e) {
            span.error(e);
            throw e;
        } finally {
            span.close();
        }
    }
}

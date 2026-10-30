package io.github.styropyr0.visionary;

import org.springframework.web.reactive.function.client.ClientRequest;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.ExchangeFunction;

import reactor.core.publisher.Mono;

/**
 * An {@code http} span per {@code WebClient} exchange. Only loaded when WebFlux
 * is on the classpath. The span joins the request that is current on the thread
 * that subscribes, which for {@code block()} in a servlet handler is the request
 * thread; exchanges subscribed on reactor threads are stored as background spans.
 * The request carries a W3C {@code traceparent} naming its span.
 */
final class WebClientSpans implements ExchangeFilterFunction {

    @Override
    public Mono<ClientResponse> filter(ClientRequest request, ExchangeFunction next) {
        return Mono.defer(() -> {
            VisionarySpan span = Telemetry.startHttp(request.method().name(), request.url());
            ClientRequest traced = traced(request, span == null ? TraceContext.current() : span.context);
            if (span == null) {
                return next.exchange(traced);
            }
            return next.exchange(traced)
                    .doOnNext(response -> Telemetry.httpStatus(span, response.statusCode().value()))
                    .doOnError(span::error)
                    .doFinally(signal -> span.close());
        });
    }

    /** The request with traceparent (and tracestate) unless it has its own; the original is immutable. */
    private static ClientRequest traced(ClientRequest request, TraceContext tc) {
        if (tc == null || request.headers().containsKey(TraceContext.TRACEPARENT)) {
            return request;
        }
        return ClientRequest.from(request).headers(h -> HttpClientSpans.inject(h, tc)).build();
    }
}

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
 */
final class WebClientSpans implements ExchangeFilterFunction {

    @Override
    public Mono<ClientResponse> filter(ClientRequest request, ExchangeFunction next) {
        return Mono.defer(() -> {
            VisionarySpan span = Telemetry.startHttp(request.method().name(), request.url());
            if (span == null) {
                return next.exchange(request);
            }
            return next.exchange(request)
                    .doOnNext(response -> Telemetry.httpStatus(span, response.statusCode().value()))
                    .doOnError(span::error)
                    .doFinally(signal -> span.close());
        });
    }
}

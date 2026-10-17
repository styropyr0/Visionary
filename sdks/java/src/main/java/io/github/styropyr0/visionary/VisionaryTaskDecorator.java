package io.github.styropyr0.visionary;

import org.springframework.core.task.TaskDecorator;

/**
 * Carries the current request over to executor threads, so spans and captured
 * errors from {@code @Async} methods and MVC async handlers join the request that
 * started them. Registered automatically when the application declares no
 * {@code TaskDecorator} of its own; otherwise call {@link Visionary#wrap(Runnable)}
 * from yours, or combine both with Spring's {@code CompositeTaskDecorator}.
 */
public final class VisionaryTaskDecorator implements TaskDecorator {

    @Override
    public Runnable decorate(Runnable runnable) {
        return Visionary.wrap(runnable);
    }
}

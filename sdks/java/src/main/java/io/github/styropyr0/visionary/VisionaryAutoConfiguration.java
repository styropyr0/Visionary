package io.github.styropyr0.visionary;

import javax.sql.DataSource;

import com.fasterxml.jackson.databind.ObjectMapper;

import jakarta.servlet.DispatcherType;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;
import org.springframework.beans.factory.BeanFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.beans.factory.config.BeanPostProcessor;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.AutoConfigurationPackages;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.web.client.RestClientCustomizer;
import org.springframework.boot.web.client.RestTemplateCustomizer;
import org.springframework.boot.web.reactive.function.client.WebClientCustomizer;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;
import org.springframework.core.env.Environment;
import org.springframework.core.task.TaskDecorator;
import org.springframework.util.ClassUtils;
import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.servlet.HandlerMapping;

/**
 * Wires Visionary into a servlet Spring Boot application. Nothing here may
 * fail startup: a missing or broken database configuration logs one warning
 * and leaves the application exactly as it was.
 */
@AutoConfiguration(afterName = "org.springframework.boot.autoconfigure.jdbc.DataSourceAutoConfiguration")
@ConditionalOnWebApplication(type = ConditionalOnWebApplication.Type.SERVLET)
@ConditionalOnClass({OncePerRequestFilter.class, HandlerMapping.class})
public class VisionaryAutoConfiguration {

    private static final Log log = LogFactory.getLog(VisionaryAutoConfiguration.class);
    private static final String HIKARI = "com.zaxxer.hikari.HikariDataSource";
    private static final String LOGBACK = "ch.qos.logback.classic.LoggerContext";

    @Bean
    VisionaryAgent visionaryAgent(Environment environment, ObjectProvider<DataSource> dataSources,
            ObjectProvider<VisionaryUserLookup> userLookup, BeanFactory beanFactory) {
        VisionaryConfig cfg = null;
        try {
            cfg = VisionaryConfig.from(environment);
            if (!cfg.databaseConfigured()) {
                log.warn("visionary: no database configured (set VISIONARY_DATABASE_URL, the D1_* variables, "
                        + "or VISIONARY_USE_APP_DATASOURCE=true); collection is off");
                return new VisionaryAgent(cfg);
            }
            if (!cfg.logRequests() && !cfg.systemEnabled() && !cfg.spans() && !cfg.errors() && !cfg.logs()) {
                return new VisionaryAgent(cfg);
            }
            ClassLoader loader = getClass().getClassLoader();
            ObjectMapper json = new ObjectMapper();
            DataSource found = dataSources.getIfUnique();
            // The application's DataSource carries statement spans; Visionary's own writes skip them.
            DataSource appDataSource = found == null ? null : JdbcSpans.unwrap(found);
            Store store = StoreFactory.create(cfg, appDataSource, json, loader);
            if (store == null) {
                return new VisionaryAgent(cfg);
            }
            Origin origin = Origin.of(cfg);
            RequestWriter writer = new RequestWriter(cfg, origin, store, json, userLookup.getIfAvailable());
            SystemSampler.PoolStats pool = appDataSource != null && ClassUtils.isPresent(HIKARI, loader)
                    ? HikariPoolStats.of(appDataSource)
                    : null;
            // Logback is the only hook for application logs; without it there is nothing to capture.
            AppLogs logs = cfg.logs() && ClassUtils.isPresent(LOGBACK, loader)
                    ? new AppLogs(store, origin, cfg, new FrameNamer(appPackages(beanFactory), contextLoader()))
                    : null;
            return new VisionaryAgent(store, origin, writer, pool, logs, cfg);
        } catch (RuntimeException | LinkageError e) {
            log.warn("visionary: disabled: " + e.getMessage());
            return new VisionaryAgent(cfg);
        }
    }

    /**
     * CPU profiles, controlled from the dashboard. Frames in the packages of the
     * application's {@code @SpringBootApplication} count as the application's own code.
     */
    @Bean
    VisionaryProfiler visionaryProfiler(VisionaryAgent agent, BeanFactory beanFactory) {
        return new VisionaryProfiler(agent, appPackages(beanFactory), contextLoader());
    }

    private static java.util.List<String> appPackages(BeanFactory beanFactory) {
        try {
            if (AutoConfigurationPackages.has(beanFactory)) {
                return AutoConfigurationPackages.get(beanFactory);
            }
        } catch (RuntimeException e) {
            // no packages: classes loaded from directories still count as the application's
        }
        return java.util.List.of();
    }

    private ClassLoader contextLoader() {
        ClassLoader loader = Thread.currentThread().getContextClassLoader();
        return loader != null ? loader : getClass().getClassLoader();
    }

    @Bean
    FilterRegistrationBean<VisionaryFilter> visionaryFilter(VisionaryAgent agent) {
        FilterRegistrationBean<VisionaryFilter> registration = new FilterRegistrationBean<>(
                new VisionaryFilter(agent.counters(), agent.requests(), agent.telemetry(), agent.logs(),
                        agent.config()));
        registration.setName("visionaryFilter");
        registration.setOrder(Ordered.HIGHEST_PRECEDENCE);
        registration.setDispatcherTypes(DispatcherType.REQUEST);
        registration.setAsyncSupported(true);
        // Boot needs a filter instance even for a registration it will skip.
        registration.setEnabled(agent.enabled());
        return registration;
    }

    /** Wraps the application's DataSource beans for {@code db} spans (not when spans or Visionary are off). */
    @Bean
    static BeanPostProcessor visionaryDataSourceSpans(Environment environment) {
        boolean on;
        try {
            VisionaryConfig cfg = VisionaryConfig.from(environment);
            on = cfg.databaseConfigured() && cfg.spans();
        } catch (RuntimeException e) {
            on = false;
        }
        boolean wrap = on;
        return new BeanPostProcessor() {
            @Override
            public Object postProcessAfterInitialization(Object bean, String beanName) {
                return wrap && bean instanceof DataSource ds ? JdbcSpans.wrap(ds) : bean;
            }
        };
    }

    /** Lets {@code @Async} and MVC async work join the request that started it. */
    @Bean
    @ConditionalOnMissingBean(TaskDecorator.class)
    VisionaryTaskDecorator visionaryTaskDecorator() {
        return new VisionaryTaskDecorator();
    }

    @Bean
    RestTemplateCustomizer visionaryRestTemplateSpans() {
        HttpClientSpans spans = new HttpClientSpans();
        return restTemplate -> {
            if (restTemplate.getInterceptors().stream().noneMatch(HttpClientSpans.class::isInstance)) {
                restTemplate.getInterceptors().add(spans);
            }
        };
    }

    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.web.client.RestClient")
    static class RestClientSpansConfiguration {

        @Bean
        RestClientCustomizer visionaryRestClientSpans() {
            HttpClientSpans spans = new HttpClientSpans();
            return builder -> builder.requestInterceptors(interceptors -> {
                if (interceptors.stream().noneMatch(HttpClientSpans.class::isInstance)) {
                    interceptors.add(spans);
                }
            });
        }
    }

    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.web.reactive.function.client.WebClient")
    static class WebClientSpansConfiguration {

        @Bean
        WebClientCustomizer visionaryWebClientSpans() {
            WebClientSpans spans = new WebClientSpans();
            return builder -> builder.filters(filters -> {
                if (filters.stream().noneMatch(WebClientSpans.class::isInstance)) {
                    filters.add(spans);
                }
            });
        }
    }

    /** Adds the Logback appender to the root logger when {@code VISIONARY_LOG_CAPTURE=true}. */
    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = {LOGBACK, "org.slf4j.LoggerFactory"})
    static class LogbackConfiguration {

        @Bean
        LogbackInstaller visionaryLogback(VisionaryAgent agent) {
            return new LogbackInstaller(agent.logs(), agent.config() != null && agent.config().logCapture());
        }
    }

    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.security.core.context.SecurityContextHolder")
    static class SecurityUserConfiguration {

        @Bean
        FilterRegistrationBean<SecurityUserFilter> visionarySecurityUserFilter(VisionaryAgent agent) {
            FilterRegistrationBean<SecurityUserFilter> registration =
                    new FilterRegistrationBean<>(new SecurityUserFilter());
            registration.setName("visionarySecurityUserFilter");
            registration.setOrder(Ordered.LOWEST_PRECEDENCE);
            registration.setDispatcherTypes(DispatcherType.REQUEST);
            registration.setAsyncSupported(true);
            registration.setEnabled(agent.enabled() && agent.requests() != null);
            return registration;
        }
    }
}

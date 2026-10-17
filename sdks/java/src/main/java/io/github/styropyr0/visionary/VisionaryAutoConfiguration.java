package io.github.styropyr0.visionary;

import javax.sql.DataSource;

import com.fasterxml.jackson.databind.ObjectMapper;

import jakarta.servlet.DispatcherType;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;
import org.springframework.core.env.Environment;
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

    @Bean
    VisionaryAgent visionaryAgent(Environment environment, ObjectProvider<DataSource> dataSources,
            ObjectProvider<VisionaryUserLookup> userLookup) {
        VisionaryConfig cfg = null;
        try {
            cfg = VisionaryConfig.from(environment);
            if (!cfg.databaseConfigured()) {
                log.warn("visionary: no database configured (set VISIONARY_DATABASE_URL, the D1_* variables, "
                        + "or VISIONARY_USE_APP_DATASOURCE=true); collection is off");
                return new VisionaryAgent(cfg);
            }
            if (!cfg.logRequests() && !cfg.systemEnabled()) {
                return new VisionaryAgent(cfg);
            }
            ClassLoader loader = getClass().getClassLoader();
            ObjectMapper json = new ObjectMapper();
            DataSource appDataSource = dataSources.getIfUnique();
            Store store = StoreFactory.create(cfg, appDataSource, json, loader);
            if (store == null) {
                return new VisionaryAgent(cfg);
            }
            RequestWriter requests = cfg.logRequests()
                    ? new RequestWriter(cfg, store, json, userLookup.getIfAvailable())
                    : null;
            SystemSampler.PoolStats pool = appDataSource != null && ClassUtils.isPresent(HIKARI, loader)
                    ? HikariPoolStats.of(appDataSource)
                    : null;
            return new VisionaryAgent(store, requests, pool, cfg);
        } catch (RuntimeException | LinkageError e) {
            log.warn("visionary: disabled: " + e.getMessage());
            return new VisionaryAgent(cfg);
        }
    }

    @Bean
    FilterRegistrationBean<VisionaryFilter> visionaryFilter(VisionaryAgent agent) {
        FilterRegistrationBean<VisionaryFilter> registration = new FilterRegistrationBean<>(
                new VisionaryFilter(agent.counters(), agent.requests(), agent.config()));
        registration.setName("visionaryFilter");
        registration.setOrder(Ordered.HIGHEST_PRECEDENCE);
        registration.setDispatcherTypes(DispatcherType.REQUEST);
        registration.setAsyncSupported(true);
        // Boot needs a filter instance even for a registration it will skip.
        registration.setEnabled(agent.enabled());
        return registration;
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
